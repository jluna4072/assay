package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// ReviewProtocol is how assay talks to a reviewer.
type ReviewProtocol string

const (
	// CommandProtocol runs a program that reads a ReviewRequest as JSON on
	// stdin and writes a response as JSON on stdout.
	CommandProtocol ReviewProtocol = "command"
)

// ReviewProtocolVersion is the version of the request and response JSON.
const ReviewProtocolVersion = 1

// DefaultReviewTimeout applies when a reviewer sets none.
const DefaultReviewTimeout = 2 * time.Minute

// ReviewerSpec is a reviewer from the project's local config. Contracts
// refer to it by name; they can't change what it runs or which model it
// uses.
type ReviewerSpec struct {
	Name     string
	Protocol ReviewProtocol
	// Command is the program for CommandProtocol, run like an approved
	// command (sandbox, cleared environment, timeout), except it can never
	// write to the project.
	Command CommandSpec
	// API configures reviewers that call an HTTP API: DecisionsProtocol,
	// TypeSafeProtocol, OpenAIProtocol, and AnthropicProtocol.
	API APISpec
}

// Describe summarizes what the reviewer runs, for listings.
func (r ReviewerSpec) Describe() string {
	switch r.Protocol {
	case CommandProtocol:
		return strings.Join(r.Command.Run, " ")
	case DecisionsProtocol, TypeSafeProtocol:
		d := r.Settings()
		return fmt.Sprintf("%s at %s, passes at %.2f", d.Model, d.Endpoint, d.Threshold)
	case OpenAIProtocol, AnthropicProtocol:
		d := r.Settings()
		return fmt.Sprintf("%s at %s", d.Model, d.Endpoint)
	}
	return ""
}

// Verdict is a reviewer's judgement of one rule.
type Verdict string

const (
	VerdictPass         Verdict = "pass"
	VerdictFail         Verdict = "fail"
	VerdictInsufficient Verdict = "insufficient_evidence"
)

// Review records one review of a rule. The exact request and response are
// stored as the rule's review_request and review_response evidence.
type Review struct {
	Reviewer    string         `json:"reviewer"`
	Protocol    ReviewProtocol `json:"protocol"`
	Model       string         `json:"model,omitempty"` // as reported by the reviewer
	Verdict     Verdict        `json:"verdict,omitempty"`
	Probability *float64       `json:"probability,omitempty"` // that the criterion is met, if the reviewer gives one
	Explanation string         `json:"explanation,omitempty"` // the reviewer's own words, verbatim
	Cites       []int          `json:"cites,omitempty"`       // evidence items the reviewer relied on
	RequestHash string         `json:"requestSha256,omitempty"`
	Duration    int64          `json:"durationMs"`
	Sandboxed   bool           `json:"sandboxed,omitempty"`
	Error       string         `json:"error,omitempty"` // why there is no verdict
}

// ReviewRequest is what a reviewer receives: the criterion and the
// recorded evidence, numbered from 1.
type ReviewRequest struct {
	Protocol int              `json:"protocol"`
	Rule     ReviewRule       `json:"rule"`
	Evidence []ReviewEvidence `json:"evidence"`
}

type ReviewRule struct {
	ID        string `json:"id"`
	Criterion string `json:"criterion"`
}

// ReviewEvidence is one evidence item with its content. Text is in Content;
// anything that isn't UTF-8 is in ContentBase64.
type ReviewEvidence struct {
	Item          int          `json:"item"`
	Kind          EvidenceKind `json:"kind"`
	Path          string       `json:"path,omitempty"`
	Paths         []string     `json:"paths,omitempty"`
	Baseline      string       `json:"baseline,omitempty"`
	Absent        bool         `json:"absent,omitempty"`
	Command       string       `json:"command,omitempty"`
	ExitCode      *int         `json:"exitCode,omitempty"`
	TimedOut      bool         `json:"timedOut,omitempty"`
	Size          int64        `json:"size"`
	SHA256        string       `json:"sha256,omitempty"`
	Content       string       `json:"content,omitempty"`
	ContentBase64 string       `json:"contentBase64,omitempty"`
}

// reviewResponse is what a reviewer returns.
type reviewResponse struct {
	Protocol    *int     `json:"protocol"`
	Verdict     string   `json:"verdict"`
	Explanation string   `json:"explanation"`
	Cites       []int    `json:"cites"`
	Model       string   `json:"model"`
	Probability *float64 `json:"probability"`
}

// reviewCall is one exchange with a reviewer.
type reviewCall struct {
	sent      []byte // exactly what was sent; nil if nothing was
	response  []byte
	duration  int64
	sandboxed bool
}

// reviewer is a client for one protocol. call sends a request in the
// protocol's own form and returns what was sent and the raw response; an
// error means there's no usable response. parse turns a response into a
// verdict, or an error.
type reviewer interface {
	call(request ReviewRequest) (reviewCall, error)
	parse(response []byte, items int, review *Review) error
}

// reviewerFor returns the client for a configured reviewer.
func (c *collector) reviewerFor(spec ReviewerSpec, config Config) (reviewer, error) {
	switch spec.Protocol {
	case CommandProtocol:
		return &commandReviewer{collector: c, spec: spec, sandbox: config.Sandbox}, nil
	case DecisionsProtocol, TypeSafeProtocol:
		return &decisionsReviewer{spec: spec, keys: c.keys}, nil
	case OpenAIProtocol:
		return &openAIReviewer{spec: spec, keys: c.keys}, nil
	case AnthropicProtocol:
		return &anthropicReviewer{spec: spec, keys: c.keys}, nil
	}
	return nil, fmt.Errorf("reviewer %q uses protocol %q, which this version of assay doesn't support", spec.Name, spec.Protocol)
}

// review asks the rule's reviewer to judge its criterion against items,
// which must already be collected. It returns the review and the request
// and response, as evidence to append after items.
func (c *collector) review(rule Rule, items []Evidence) (Review, []Evidence) {
	review := Review{Reviewer: rule.Reviewer}
	config, err := c.config()
	if err != nil {
		review.Error = err.Error()
		return review, nil
	}
	spec, err := config.Reviewer(rule.Reviewer)
	if err != nil {
		review.Error = err.Error()
		return review, nil
	}
	review.Reviewer, review.Protocol = spec.Name, spec.Protocol
	request, err := c.reviewRequest(rule, items)
	if err != nil {
		review.Error = err.Error()
		return review, nil
	}
	client, err := c.reviewerFor(spec, config)
	if err != nil {
		review.Error = err.Error()
		return review, nil
	}
	return c.send(review, client, request)
}

// send makes one review call and records it.
func (c *collector) send(review Review, client reviewer, request ReviewRequest) (Review, []Evidence) {
	call, err := client.call(request)
	review.Duration, review.Sandboxed = call.duration, call.sandboxed
	var recorded []Evidence
	if call.sent != nil {
		requestItem := Evidence{Kind: ReviewRequestEvidence, Reviewer: review.Reviewer}
		requestItem.store(call.sent, c.blobs)
		review.RequestHash = requestItem.SHA256
		recorded = append(recorded, requestItem)
	}
	if call.response != nil {
		responseItem := Evidence{Kind: ReviewResponseEvidence, Reviewer: review.Reviewer}
		responseItem.store(call.response, c.blobs)
		recorded = append(recorded, responseItem)
	}
	if err != nil {
		review.Error = err.Error()
		return review, recorded
	}
	if err := client.parse(call.response, len(request.Evidence), &review); err != nil {
		review.Error = fmt.Sprintf("%s's response isn't usable: %v", review.Reviewer, err)
	}
	return review, recorded
}

// reviewRequest builds the request for rule from its collected evidence.
// Evidence that couldn't be collected, or was too large to store whole,
// isn't sent: a reviewer must see everything the rule asks for.
func (c *collector) reviewRequest(rule Rule, items []Evidence) (ReviewRequest, error) {
	request := ReviewRequest{Protocol: ReviewProtocolVersion, Rule: ReviewRule{ID: rule.ID, Criterion: rule.Criterion}, Evidence: []ReviewEvidence{}}
	for i, item := range items {
		switch {
		case item.Error != "":
			return ReviewRequest{}, fmt.Errorf("evidence item %d (%s) couldn't be collected, so it can't be reviewed: %s", i+1, item.Kind, item.Error)
		case item.Truncated:
			return ReviewRequest{}, fmt.Errorf("evidence item %d (%s) is larger than %d bytes, so it can't be reviewed whole", i+1, item.Kind, MaxEvidenceBytes)
		}
		entry := ReviewEvidence{
			Item: i + 1, Kind: item.Kind, Path: item.Path, Paths: item.Paths, Baseline: item.Baseline,
			Absent: item.Absent, Command: item.Command, TimedOut: item.TimedOut, Size: item.Size, SHA256: item.SHA256,
		}
		if item.Kind == CommandEvidence {
			exitCode := item.ExitCode
			entry.ExitCode = &exitCode
		}
		if !item.Absent {
			content, ok := c.blobs[item.Blob]
			if !ok {
				return ReviewRequest{}, fmt.Errorf("evidence item %d's content is missing", i+1)
			}
			if utf8.Valid(content) {
				entry.Content = string(content)
			} else {
				entry.ContentBase64 = base64.StdEncoding.EncodeToString(content)
			}
		}
		request.Evidence = append(request.Evidence, entry)
	}
	return request, nil
}

// parseReview checks a reviewer's response and fills in the verdict.
// Anything unexpected is an error, never a verdict.
func parseReview(data []byte, items int, review *Review) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var response reviewResponse
	if err := decoder.Decode(&response); err != nil {
		return fmt.Errorf("expected one JSON object: %v", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected one JSON object, found more after it")
	}
	if response.Protocol == nil || *response.Protocol != ReviewProtocolVersion {
		return fmt.Errorf(`"protocol" must be %d`, ReviewProtocolVersion)
	}
	return checkVerdict(response, items, review)
}

// checkVerdict validates a verdict, its citations, and any probability, and
// records them in review.
func checkVerdict(response reviewResponse, items int, review *Review) error {
	switch verdict := Verdict(response.Verdict); verdict {
	case VerdictPass, VerdictFail, VerdictInsufficient:
		review.Verdict = verdict
	case "":
		return fmt.Errorf(`"verdict" is missing`)
	default:
		return fmt.Errorf(`unknown verdict %q; expected pass, fail, or insufficient_evidence`, response.Verdict)
	}
	for _, cite := range response.Cites {
		if cite < 1 || cite > items {
			return fmt.Errorf("cites evidence item %d, but the request had %s", cite, countItems(items))
		}
	}
	if p := response.Probability; p != nil && (math.IsNaN(*p) || *p < 0 || *p > 1) {
		return fmt.Errorf(`"probability" must be between 0 and 1`)
	}
	review.Explanation, review.Cites, review.Model, review.Probability = response.Explanation, response.Cites, response.Model, response.Probability
	return nil
}

func countItems(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// reviewVerdict turns a review into the rule's status and reason. Only a
// pass passes; every failure to get a verdict is an error.
func reviewVerdict(review Review) (ResultStatus, string) {
	explain := func(fallback string) string {
		if review.Explanation != "" {
			return review.Reviewer + ": " + review.Explanation
		}
		return review.Reviewer + ": " + fallback
	}
	switch {
	case review.Error != "":
		return OperationalError, review.Error
	case review.Verdict == VerdictPass:
		return Passed, explain("criterion met")
	case review.Verdict == VerdictInsufficient:
		return Failed, explain("not enough evidence to decide") + " (insufficient evidence)"
	case review.Verdict == VerdictFail:
		return Failed, explain("criterion not met")
	}
	return OperationalError, fmt.Sprintf("%s returned no verdict", review.Reviewer)
}

// commandReviewer runs a reviewer program per request.
type commandReviewer struct {
	collector *collector
	spec      ReviewerSpec
	sandbox   bool
}

func (r *commandReviewer) parse(response []byte, items int, review *Review) error {
	return parseReview(response, items, review)
}

func (r *commandReviewer) call(request ReviewRequest) (reviewCall, error) {
	var call reviewCall
	body, err := json.Marshal(request)
	if err != nil {
		return call, err
	}
	spec := r.spec.Command
	spec.Name = r.spec.Name
	spec.WritesProject = false
	spec.Dir = ""
	if spec.Timeout <= 0 {
		spec.Timeout = DefaultReviewTimeout
	}
	scratch, err := r.collector.scratchDir("reviewer", r.spec.Name)
	if err != nil {
		return call, err
	}
	stdout := &limitedBuffer{limit: MaxEvidenceBytes}
	stderr := &limitedBuffer{limit: 64 << 10}
	result, err := runProcess(process{
		spec: spec, sandbox: r.sandbox, project: r.collector.projectDir, state: filepath.Join(r.collector.root, ".assay"), scratch: scratch,
		cache: filepath.Join(r.collector.root, ".assay", "reviewer-cache", r.spec.Name),
		stdin: body, stdout: stdout, stderr: stderr,
	})
	call.duration, call.sandboxed = result.duration, result.sandboxed
	if err != nil {
		return call, err // the program didn't start, so nothing was sent
	}
	call.sent = body
	call.response = stdout.Bytes()
	switch {
	case result.timedOut:
		return call, fmt.Errorf("reviewer %s timed out after %s", r.spec.Name, spec.Timeout)
	case result.exitCode != 0:
		return call, fmt.Errorf("reviewer %s exited with status %d%s", r.spec.Name, result.exitCode, stderrTail(stderr.Bytes()))
	case stdout.overflow:
		return call, fmt.Errorf("reviewer %s wrote more than %d bytes", r.spec.Name, MaxEvidenceBytes)
	}
	return call, nil
}

// limitedBuffer keeps the first limit bytes written to it. It doesn't embed
// bytes.Buffer: io.Copy would use the buffer's ReadFrom and skip the limit.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room < len(p) {
		b.overflow = true
		b.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// stderrTail formats the end of a program's stderr for an error message.
func stderrTail(stderr []byte) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return ""
	}
	if len(text) > 500 {
		text = "…" + text[len(text)-500:]
	}
	return ": " + strings.ToValidUTF8(text, "")
}

// ReviewSample is one fixed review assay runs to test a reviewer: a
// criterion and a file that either meets it or doesn't.
type ReviewSample struct {
	Name      string `json:"name"`
	Criterion string `json:"criterion"`
	Content   string `json:"content"`
	WantPass  bool   `json:"wantPass"`
	Review    Review `json:"review"`
}

// OK reports whether the reviewer judged the sample as expected.
func (s ReviewSample) OK() bool {
	return s.Review.Error == "" && (s.Review.Verdict == VerdictPass) == s.WantPass
}

// TestReviewer sends a configured reviewer (the default when name is empty)
// one sample that meets its criterion and one that doesn't. The error is
// non-nil when the reviewer can't be found or set up; problems with its
// answers are in each sample's Review.
func (s *Service) TestReviewer(projectDir, name string) (ReviewerSpec, []ReviewSample, error) {
	c := newCollector(projectDir, projectDir, "", s.LoadConfig)
	c.keys = s.Keys
	defer c.cleanup()
	config, err := c.config()
	if err != nil {
		return ReviewerSpec{}, nil, err
	}
	spec, err := config.Reviewer(name)
	if err != nil {
		return ReviewerSpec{}, nil, err
	}
	client, err := c.reviewerFor(spec, config)
	if err != nil {
		return spec, nil, err
	}
	const criterion = "greeting.txt greets the reader by saying hello"
	samples := []ReviewSample{
		{Name: "meets the criterion", Criterion: criterion, Content: "Hello, reader! Welcome to the project.\n", WantPass: true},
		{Name: "doesn't meet the criterion", Criterion: criterion, Content: "Goodbye. This project is closed.\n", WantPass: false},
	}
	for i, sample := range samples {
		item := Evidence{Kind: FileEvidence, Path: "greeting.txt"}
		item.store([]byte(sample.Content), c.blobs)
		rule := Rule{ID: "sample", Criterion: sample.Criterion, Kind: ReviewCheck}
		request, err := c.reviewRequest(rule, []Evidence{item})
		if err != nil {
			return spec, nil, err
		}
		samples[i].Review, _ = c.send(Review{Reviewer: spec.Name, Protocol: spec.Protocol}, client, request)
	}
	return spec, samples, nil
}
