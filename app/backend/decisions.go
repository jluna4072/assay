package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DecisionsProtocol calls a decision model, such as TypeSafe's Jev, through
// an OpenAI-compatible Decisions API (POST <endpoint>/decisions). The model
// answers one yes/no question with the probability that the criterion is
// met; the rule passes when it reaches the reviewer's threshold.
const DecisionsProtocol ReviewProtocol = "decisions"

// TypeSafeProtocol calls TypeSafe's own API (POST <endpoint>/systemone)
// with a TypeSafe console key. It asks the same yes/no question as
// DecisionsProtocol, in TypeSafe's format.
const TypeSafeProtocol ReviewProtocol = "typesafe"

// Defaults for a typesafe reviewer: Jev from TypeSafe directly.
const (
	DefaultTypeSafeEndpoint = "https://api.typesafe.ai/v1"
	DefaultTypeSafeModel    = "jev-latest"
	DefaultTypeSafeKeyEnv   = "TYPESAFE_API_KEY"
)

// protocolDefaults are each HTTP protocol's default settings. A protocol
// without a default model needs one in its config.
var protocolDefaults = map[ReviewProtocol]APISpec{
	DecisionsProtocol: {Endpoint: DefaultDecisionsEndpoint, Model: DefaultDecisionsModel, KeyEnv: DefaultDecisionsKeyEnv},
	TypeSafeProtocol:  {Endpoint: DefaultTypeSafeEndpoint, Model: DefaultTypeSafeModel, KeyEnv: DefaultTypeSafeKeyEnv},
	OpenAIProtocol:    {Endpoint: DefaultOpenAIEndpoint, KeyEnv: DefaultOpenAIKeyEnv},
	AnthropicProtocol: {Endpoint: DefaultAnthropicEndpoint, Model: DefaultAnthropicModel, KeyEnv: DefaultAnthropicKeyEnv},
}

// UsesAPIKey reports whether a reviewer's protocol calls an HTTP API with
// an API key that assay looks up.
func UsesAPIKey(protocol ReviewProtocol) bool {
	_, ok := protocolDefaults[protocol]
	return ok
}

// DecisionModel reports whether a protocol's model answers with a
// probability, judged against a threshold, rather than a written verdict.
func DecisionModel(protocol ReviewProtocol) bool {
	return protocol == DecisionsProtocol || protocol == TypeSafeProtocol
}

// Settings returns the reviewer's API settings with its protocol's
// defaults filled in.
func (r ReviewerSpec) Settings() APISpec {
	d := r.API
	defaults := protocolDefaults[r.Protocol]
	if d.Endpoint == "" {
		d.Endpoint = defaults.Endpoint
	}
	if d.Model == "" {
		d.Model = defaults.Model
	}
	if d.KeyEnv == "" {
		d.KeyEnv = defaults.KeyEnv
	}
	return d.WithDefaults()
}

// Defaults for a decisions reviewer: TypeSafe's Jev through Vercel's AI
// Gateway.
const (
	DefaultDecisionsEndpoint = "https://ai-gateway.vercel.sh/v1"
	DefaultDecisionsModel    = "typesafe-ai/jev"
	DefaultDecisionsKeyEnv   = "AI_GATEWAY_API_KEY"
	DefaultDecisionThreshold = 0.9
)

// MaxDecisionInput bounds the request assay sends to a decision model.
// Jev reads about 32,000 tokens of input; larger evidence is an error, not
// a review of part of it.
const MaxDecisionInput = 128 << 10

// APISpec configures a reviewer that calls an HTTP API.
type APISpec struct {
	Endpoint    string        // API base URL, without the request path
	Model       string        // model ID
	KeyEnv      string        // environment variable holding the API key
	Threshold   float64       // decision models: probability needed to pass
	Timeout     time.Duration // DefaultReviewTimeout when zero
	Temperature *float64      // openai: sampling temperature, when set
	Effort      string        // anthropic: effort level, when set
}

// WithDefaults fills in unset fields with DecisionsProtocol's defaults.
// ReviewerSpec.Settings applies the right defaults for each protocol.
func (d APISpec) WithDefaults() APISpec {
	if d.Endpoint == "" {
		d.Endpoint = DefaultDecisionsEndpoint
	}
	d.Endpoint = strings.TrimRight(d.Endpoint, "/")
	if d.Model == "" {
		d.Model = DefaultDecisionsModel
	}
	if d.KeyEnv == "" {
		d.KeyEnv = DefaultDecisionsKeyEnv
	}
	if d.Threshold == 0 {
		d.Threshold = DefaultDecisionThreshold
	}
	if d.Timeout <= 0 {
		d.Timeout = DefaultReviewTimeout
	}
	return d
}

// Host is the endpoint's host name. API keys are stored per host, so
// reviewers on the same provider share one.
func (d APISpec) Host() string {
	u, err := url.Parse(d.WithDefaults().Endpoint)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// Local reports whether the endpoint is on this machine, where a key is
// optional.
func (d APISpec) Local() bool {
	host := d.Host()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckEndpoint reports a problem with an endpoint URL: it must be https,
// except on this machine, so keys aren't sent in the clear.
func CheckEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("endpoint must be a URL like %s", DefaultDecisionsEndpoint)
	}
	if u.Scheme == "http" && !(APISpec{Endpoint: endpoint}).Local() {
		return errors.New("endpoint must use https unless it's on this machine")
	}
	return nil
}

// KeySource finds the API key for a reviewer. It returns "" with no error
// when the reviewer works without one (a local server).
type KeySource interface {
	Key(spec ReviewerSpec) (string, error)
}

// EnvKeys reads keys only from each reviewer's environment variable.
type EnvKeys struct{}

func (EnvKeys) Key(spec ReviewerSpec) (string, error) {
	decisions := spec.Settings()
	if key := strings.TrimSpace(lookupEnv(decisions.KeyEnv)); key != "" {
		return key, nil
	}
	if decisions.Local() {
		return "", nil
	}
	return "", fmt.Errorf("reviewer %s has no API key: set %s", spec.Name, decisions.KeyEnv)
}

// lookupEnv is a variable so tests can supply an environment.
var lookupEnv = os.Getenv

// decisionsReviewer calls a Decisions API.
type decisionsReviewer struct {
	spec ReviewerSpec
	keys KeySource
}

// The question assay asks. The criterion is part of the question; the
// evidence is the input.
const (
	decisionQuestion = "criterion_met"
	decisionPreamble = "Evidence recorded by assay, an automated checker, for one acceptance criterion. Judge only this evidence; don't assume anything it doesn't show."
)

type decisionsRequest struct {
	Model     string                 `json:"model"`
	Input     []decisionsMessage     `json:"input"`
	Questions []decisionQuestionJSON `json:"questions"`
}

type decisionsMessage struct {
	Role    string          `json:"role"`
	Content []decisionsPart `json:"content"`
}

type decisionsPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Data any    `json:"data,omitempty"`
}

type decisionQuestionJSON struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

type decisionsResponse struct {
	Model   string `json:"model"`
	Answers []struct {
		Type        string   `json:"type"`
		Name        *string  `json:"name"`
		Probability *float64 `json:"probability"`
	} `json:"answers"`
}

// typeSafeRequest is TypeSafe's own request format: the evidence is the
// state, and questions are keyed by name.
type typeSafeRequest struct {
	Model     string                      `json:"model"`
	State     any                         `json:"state"`
	Questions map[string]typeSafeQuestion `json:"questions"`
}

type typeSafeQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type typeSafeResponse struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type string   `json:"type"`
		Noul *float64 `json:"noul"`
	} `json:"answers"`
}

// body builds the request in the reviewer's protocol, and returns where to
// send it.
func (r *decisionsReviewer) body(request ReviewRequest, spec APISpec) (string, []byte, error) {
	question := "Based only on the evidence, is this acceptance criterion met? Criterion: " + request.Rule.Criterion
	if r.spec.Protocol == TypeSafeProtocol {
		body, err := json.Marshal(typeSafeRequest{
			Model: spec.Model,
			State: map[string]any{"about": decisionPreamble, "evidence": request.Evidence},
			Questions: map[string]typeSafeQuestion{
				decisionQuestion: {Type: "noul", Instructions: question},
			},
		})
		return spec.Endpoint + "/systemone", body, err
	}
	body, err := json.Marshal(decisionsRequest{
		Model: spec.Model,
		Input: []decisionsMessage{{Role: "user", Content: []decisionsPart{
			{Type: "input_text", Text: decisionPreamble},
			{Type: "input_json", Data: map[string]any{"evidence": request.Evidence}},
		}}},
		Questions: []decisionQuestionJSON{{Type: "predicate", Name: decisionQuestion, Instructions: question}},
	})
	return spec.Endpoint + "/decisions", body, err
}

func (r *decisionsReviewer) call(request ReviewRequest) (reviewCall, error) {
	var call reviewCall
	spec := r.spec.Settings()
	target, body, err := r.body(request, spec)
	if err != nil {
		return call, err
	}
	if len(body) > MaxDecisionInput {
		return call, fmt.Errorf("the evidence is too large for %s: the request would be %d bytes, more than the %d it accepts (about 32,000 tokens)", r.spec.Name, len(body), MaxDecisionInput)
	}
	return postJSON(r.spec, r.keys, target, body, nil)
}

// postJSON sends body to target with the reviewer's API key as a bearer
// token (or with headers set by authorize, when given), and returns the
// call with the response. HTTP errors are errors; a rejected key says how
// to fix it.
func postJSON(reviewer ReviewerSpec, keys KeySource, target string, body []byte, authorize func(*http.Request, string)) (reviewCall, error) {
	var call reviewCall
	spec := reviewer.Settings()
	if keys == nil {
		keys = EnvKeys{}
	}
	key, err := keys.Key(reviewer)
	if err != nil {
		return call, err
	}

	httpRequest, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return call, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", "assay")
	switch {
	case authorize != nil:
		authorize(httpRequest, key)
	case key != "":
		httpRequest.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: spec.Timeout}
	started := time.Now()
	call.sent = body
	response, err := client.Do(httpRequest)
	call.duration = time.Since(started).Milliseconds()
	if err != nil {
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			return call, fmt.Errorf("reviewer %s timed out after %s", reviewer.Name, spec.Timeout)
		}
		return call, fmt.Errorf("reviewer %s couldn't be reached at %s: %v", reviewer.Name, spec.Endpoint, unwrapURLError(err))
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxEvidenceBytes+1))
	call.duration = time.Since(started).Milliseconds()
	if err != nil {
		return call, fmt.Errorf("reviewer %s: read response: %v", reviewer.Name, err)
	}
	call.response = data
	if len(data) > MaxEvidenceBytes {
		call.response = data[:MaxEvidenceBytes]
		return call, fmt.Errorf("reviewer %s returned more than %d bytes", reviewer.Name, MaxEvidenceBytes)
	}
	return call, httpStatusError(reviewer, key, response.StatusCode, data)
}

// httpStatusError describes a non-200 response, or returns nil.
func httpStatusError(reviewer ReviewerSpec, key string, status int, data []byte) error {
	spec := reviewer.Settings()
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// Name the service, not the model: a key for one provider sent to
		// another (a TypeSafe key to a gateway, say) fails here. A key from
		// the environment variable overrides a saved one, so logging in
		// again wouldn't help; say to fix the variable instead.
		fix := fmt.Sprintf("replace it with \"assay reviewer login %s\"", reviewer.Name)
		if env := strings.TrimSpace(lookupEnv(spec.KeyEnv)); env != "" && env == key {
			fix = fmt.Sprintf("fix or unset %s, which overrides any key saved with \"assay reviewer login\"", spec.KeyEnv)
		}
		return fmt.Errorf("reviewer %s: %s rejected the API key (HTTP %d%s). Check that it's a key for %s, then %s", reviewer.Name, spec.Host(), status, apiMessage(data), spec.Host(), fix)
	case status != http.StatusOK:
		return fmt.Errorf("%s returned HTTP %d%s", reviewer.Name, status, apiMessage(data))
	}
	return nil
}

func (r *decisionsReviewer) parse(data []byte, _ int, review *Review) error {
	spec := r.spec.Settings()
	p, model, err := r.probability(data)
	if err != nil {
		return err
	}
	review.Probability = &p
	review.Model = firstNonEmpty(model, spec.Model)
	if p >= spec.Threshold {
		review.Verdict = VerdictPass
		review.Explanation = fmt.Sprintf("probability %.2f that the criterion is met", p)
	} else {
		review.Verdict = VerdictFail
		review.Explanation = fmt.Sprintf("probability %.2f that the criterion is met; passing needs %.2f", p, spec.Threshold)
	}
	return nil
}

// probability reads the yes probability and model from a response in the
// reviewer's protocol.
func (r *decisionsReviewer) probability(data []byte) (float64, string, error) {
	check := func(p *float64, field string) (float64, error) {
		switch {
		case p == nil:
			return 0, fmt.Errorf("the answer has no %q", field)
		case math.IsNaN(*p) || *p < 0 || *p > 1:
			return 0, fmt.Errorf("%q must be between 0 and 1", field)
		}
		return *p, nil
	}
	if r.spec.Protocol == TypeSafeProtocol {
		var response typeSafeResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return 0, "", fmt.Errorf("expected a TypeSafe response: %v", err)
		}
		answer, ok := response.Answers[decisionQuestion]
		switch {
		case len(response.Answers) != 1 || !ok:
			return 0, "", fmt.Errorf("expected one answer, to %q", decisionQuestion)
		case answer.Type == "refusal":
			return 0, "", errors.New("the model declined to answer")
		case answer.Type != "noul":
			return 0, "", fmt.Errorf("expected a noul answer, found %q", answer.Type)
		}
		p, err := check(answer.Noul, "noul")
		return p, response.Model, err
	}
	var response decisionsResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return 0, "", fmt.Errorf("expected a Decisions API response: %v", err)
	}
	if len(response.Answers) != 1 {
		return 0, "", fmt.Errorf("expected 1 answer, found %d", len(response.Answers))
	}
	answer := response.Answers[0]
	switch {
	case answer.Type == "refusal":
		return 0, "", errors.New("the model declined to answer")
	case answer.Type != "predicate":
		return 0, "", fmt.Errorf("expected a predicate answer, found %q", answer.Type)
	case answer.Name != nil && *answer.Name != decisionQuestion:
		return 0, "", fmt.Errorf("answered question %q, not %q", *answer.Name, decisionQuestion)
	}
	p, err := check(answer.Probability, "probability")
	return p, response.Model, err
}

// apiMessage extracts an error message from an API error body, in either
// {"error": {"message": ...}} or {"message": ...} form.
func apiMessage(data []byte) string {
	var body struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
		Detail json.RawMessage `json:"detail"` // TypeSafe: a string, or a list of {msg}
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	message := body.Message
	if body.Error != nil && body.Error.Message != "" {
		message = body.Error.Message
	}
	if message == "" && len(body.Detail) > 0 {
		var text string
		var list []struct {
			Msg string `json:"msg"`
			Loc []any  `json:"loc"`
		}
		if json.Unmarshal(body.Detail, &text) == nil {
			message = text
		} else if json.Unmarshal(body.Detail, &list) == nil {
			parts := []string{}
			for _, item := range list {
				parts = append(parts, item.Msg)
			}
			message = strings.Join(parts, "; ")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 300 {
		message = message[:300] + "…"
	}
	if message == "" {
		return ""
	}
	return ": " + strings.ToValidUTF8(message, "")
}

// unwrapURLError drops net/http's "Post <url>:" prefix, which repeats the
// endpoint.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
