// Package docker finds newer tags for the base images in Dockerfiles using the
// OCI distribution API, which Docker Hub, GHCR, GCR/Artifact Registry, ECR
// Public, Quay and registry.k8s.io all implement.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Image identifies a repository in a registry.
type Image struct {
	Registry   string // e.g. docker.io, ghcr.io
	Repository string // e.g. library/golang
}

// ParseImage splits "golang", "ghcr.io/org/app" or "localhost:5000/app" into
// registry and repository using the same rules as the Docker CLI.
func ParseImage(name string) Image {
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return Image{Registry: first, Repository: rest}
	}
	if !found {
		return Image{Registry: "docker.io", Repository: "library/" + name}
	}
	return Image{Registry: "docker.io", Repository: name}
}

// Registry lists tags. BaseURL maps a registry host to its API base URL and
// is replaceable in tests.
type Registry struct {
	HTTP    *http.Client
	BaseURL func(registry string) string
}

func (r Registry) base(registry string) string {
	if r.BaseURL != nil {
		return r.BaseURL(registry)
	}
	if registry == "docker.io" {
		registry = "registry-1.docker.io"
	}
	return "https://" + registry
}

func (r Registry) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Tags lists every tag of an image, following pagination.
func (r Registry) Tags(ctx context.Context, img Image) ([]string, error) {
	next := r.base(img.Registry) + "/v2/" + img.Repository + "/tags/list?n=1000"
	token := ""
	var tags []string
	for page := 0; next != "" && page < 20; page++ {
		resp, err := r.get(ctx, next, token)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && token == "" {
			challenge := resp.Header.Get("WWW-Authenticate")
			_ = resp.Body.Close()
			if token, err = r.anonymousToken(ctx, challenge); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", img.Registry, img.Repository, err)
			}
			page--
			continue
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = decode(resp, &body)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", img.Registry, img.Repository, err)
		}
		tags = append(tags, body.Tags...)
		next = nextPage(r.base(img.Registry), resp.Header.Get("Link"))
	}
	return tags, nil
}

func (r Registry) get(ctx context.Context, u, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return r.client().Do(req)
}

func decode(resp *http.Response, v any) error {
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, v)
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// anonymousToken performs the Docker token flow from a WWW-Authenticate
// challenge such as: Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/golang:pull".
func (r Registry) anonymousToken(ctx context.Context, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("unsupported auth challenge %q", challenge)
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return "", fmt.Errorf("auth challenge without realm: %q", challenge)
	}
	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	resp, err := r.get(ctx, params["realm"]+"?"+q.Encode(), "")
	if err != nil {
		return "", err
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := decode(resp, &tok); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	if tok.Token != "" {
		return tok.Token, nil
	}
	return tok.AccessToken, nil
}

// nextPage resolves a Link: </v2/...>; rel="next" header against base.
func nextPage(base, link string) string {
	for _, part := range strings.Split(link, ",") {
		segs := strings.Split(part, ";")
		if len(segs) < 2 || !strings.Contains(segs[1], `rel="next"`) {
			continue
		}
		target := strings.Trim(strings.TrimSpace(segs[0]), "<>")
		if strings.HasPrefix(target, "/") {
			return base + target
		}
		return target
	}
	return ""
}
