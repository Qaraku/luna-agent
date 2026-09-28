package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// maxModelsResponse bounds what Luna will read from an endpoint it was pointed
// at. A model list is a few kilobytes; anything past this is not one, and reading
// it whole would let a wrong URL (or a hostile one) decide how much memory this
// process spends.
const maxModelsResponse = 1 << 20

// maxModelsBodyInError bounds how much of a failed response reaches a reader. An
// error message is read by a person and printed in a browser, so it carries the
// provider's own words — truncated, because a provider that answers with an HTML
// page should not paste it into the interface.
const maxModelsBodyInError = 200

// ListModels asks an OpenAI-compatible endpoint which models it serves.
//
// The request is made by the server and never by the browser: the key belongs to
// this installation, and a page that could ask Luna to call a provider with it
// would be a page that could use it.
//
// The endpoint is called at <baseURL>/models, the same base the chat client is
// built from, so a base URL that already ends in /v1 answers at /v1/models —
// there is one rule for where requests go, not two.
func ListModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("no endpoint to ask: set the base URL first")
	}
	endpoint := base + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build the request for %s: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponse))
	if err != nil {
		return nil, fmt.Errorf("read the response from %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s: %s", endpoint, resp.Status, snippet(body))
	}
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, fmt.Errorf("%s did not answer with a model list: %w", endpoint, err)
	}
	models := make([]string, 0, len(listed.Data))
	seen := map[string]bool{}
	for _, entry := range listed.Data {
		name := strings.TrimSpace(entry.ID)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, name)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%s answered with no models; the endpoint may not be OpenAI-compatible, or the key may not be allowed to list them", endpoint)
	}
	sort.Strings(models)
	return models, nil
}

// snippet is the beginning of a response body, on one line, for an error message.
func snippet(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > maxModelsBodyInError {
		return text[:maxModelsBodyInError] + "…"
	}
	if text == "" {
		return "(empty response)"
	}
	return text
}
