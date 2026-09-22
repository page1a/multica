package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/service"
)

// The transfer import is a long upload over a link nobody controls, in front of
// a CDN edge that cuts a request it has not finished in 100 seconds. Both facts
// shape this file:
//
//   - The body is compressed unless the target says it cannot decode it, which
//     is worth 5–10× on the JSON the transfer sends.
//   - Every request is packed to a byte budget rather than to a row count, so
//     one request finishes in tens of seconds instead of minutes.
//   - A request the edge drops is halved and retried, because the write side is
//     idempotent (deterministic ids, `ON CONFLICT DO NOTHING`).
//   - A request that runs out of halves says so in terms a user can act on:
//     how many bytes it carried, how fast the link really is, and what else can
//     be done.
//
// Before this, one ~10 MB request was the only shape the import could produce,
// and the failure it produced named the wrong thing: a 5xx rendered as "the
// Multica service is temporarily unavailable", which sends the reader to
// inspect a server that is perfectly healthy.

const (
	// transferStatusEdgeConnectionTimeout / transferStatusEdgeTimeout are the
	// non-standard statuses Cloudflare answers when its own proxy window
	// closes; they are the whole reason the message below exists, so they are
	// named rather than compared against bare numbers.
	transferStatusEdgeConnectionTimeout = 522
	transferStatusEdgeTimeout           = 524

	transferJSONContentType = "application/json"
)

// transferWireTarget is what the import knows about the instance it uploads to.
type transferWireTarget struct {
	// probed reports whether the target answered the capability question. A
	// false value is not an error: an old instance, a network hiccup and a
	// host that is not a Multica server all land here, and the import still
	// has to work against them.
	probed bool
	caps   *service.TransferCapabilities
	host   string
}

// gzip reports whether bodies may be sent compressed.
func (t transferWireTarget) gzip() bool {
	return t.caps != nil && t.caps.AcceptsGzip
}

func (t transferWireTarget) label() string {
	if t.host == "" {
		return "the target instance"
	}
	return t.host
}

// probeTransferWireTarget asks the target what one request may look like.
//
// The probe is advisory in both directions: a target that cannot be asked gets
// the conservative defaults (no compression, the default budget), and a target
// that answers is still only describing itself — the first real request is what
// measures the link.
func probeTransferWireTarget(ctx context.Context, baseURL string, warn io.Writer) transferWireTarget {
	target := transferWireTarget{host: baseURL}
	probeCtx, cancel := context.WithTimeout(ctx, transferTargetProbeTimeout)
	defer cancel()
	caps, err := service.ProbeTransferCapabilities(probeCtx, &http.Client{Timeout: transferTargetProbeTimeout}, baseURL)
	if err != nil {
		fmt.Fprintf(warn, "Could not ask %s what it accepts for a transfer request (%v); sending uncompressed bodies within the default --max-request-bytes budget.\n", baseURL, err)
		return target
	}
	target.probed = true
	target.caps = &caps
	if !caps.AcceptsGzip {
		fmt.Fprintf(warn, "%s does not read gzip request bodies; sending them uncompressed. Upgrading that instance to a build that advertises accepts_gzip makes this migration several times faster.\n", baseURL)
	}
	return target
}

// transferWireChunk is one request body on its way to the target.
type transferWireChunk struct {
	// Body is the exact payload to POST: JSON, or a multipart form.
	Body []byte
	// ContentType travels as the Content-Type header.
	ContentType string
	// Label names what this chunk carries. It is what the failure message
	// points at when the chunk cannot be divided any further.
	Label string
	// Split divides the chunk in two, for the case where the edge drops it.
	// A nil Split means the body is atomic — a single row, a single
	// attachment — and halving it is not possible.
	Split func() (transferWireChunk, transferWireChunk, bool)
}

// transferWireSender posts transfer bodies and remembers what the link did.
//
// It owns three things PostJSON cannot: the compression decision, the request
// budget (the value the chunk planners pack to), and the retry that halves a
// dropped request. Progress goes out through the same stderr channel the
// Desktop migration card reads.
type transferWireSender struct {
	client   *cli.APIClient
	target   transferWireTarget
	progress *transferProgressReporter

	// configured is `--max-request-bytes`, 0 meaning "the default".
	configured int
	// rate is the measured upload throughput in bytes per second, an EWMA over
	// the requests that succeeded. 0 until the first one answers.
	rate float64

	stage   string
	sent    int
	planned int
}

func newTransferWireSender(client *cli.APIClient, target transferWireTarget, configured int, progress *transferProgressReporter) *transferWireSender {
	return &transferWireSender{client: client, target: target, configured: configured, progress: progress}
}

// compresses reports whether the bodies this sender produces will travel
// compressed, which is what the chunk planners must size against.
func (s *transferWireSender) compresses() bool {
	return s.target.gzip()
}

// chunkLimit is the byte budget one request may carry, compressed.
func (s *transferWireSender) chunkLimit() int {
	return service.TransferWireLimit(s.configured, s.target.caps, s.rate)
}

// beginStage starts a progress stage and declares how many requests it plans to
// send. A halving retry adds to that number rather than hiding from it.
func (s *transferWireSender) beginStage(stage string, planned int) {
	s.stage = stage
	s.planned = planned
	s.sent = 0
}

// post sends one chunk, collecting every response body it produces: a chunk the
// edge cut in half answers twice, and the caller folds both reports.
func (s *transferWireSender) post(ctx context.Context, path string, chunk transferWireChunk) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := s.postDepth(ctx, path, chunk, 0, &out)
	return out, err
}

func (s *transferWireSender) postDepth(ctx context.Context, path string, chunk transferWireChunk, depth int, out *[]json.RawMessage) error {
	payload := chunk.Body
	encoding := ""
	if s.target.gzip() {
		// gzipBytes reports whether it actually compressed. A body it could not
		// shrink (an already-compressed attachment) stays raw, and the header
		// has to follow the bytes rather than the target's capability — a
		// Content-Encoding over a body that is not that encoding is a 400.
		var compressed bool
		payload, compressed = gzipBytes(chunk.Body)
		if compressed {
			encoding = "gzip"
		}
	}
	start := time.Now()
	raw, err := s.do(ctx, path, chunk.ContentType, encoding, payload)
	elapsed := time.Since(start)
	if err == nil {
		s.observe(len(payload), elapsed)
		s.sent++
		s.report(len(payload))
		if out != nil {
			*out = append(*out, raw)
		}
		return nil
	}
	var first, second transferWireChunk
	splittable := false
	if ctx.Err() == nil && transferWireEdgeFailure(err) && depth < service.TransferWireSplitLimit && chunk.Split != nil {
		first, second, splittable = chunk.Split()
	}
	if !splittable {
		return s.fail(chunk, payload, elapsed, depth, err)
	}
	// The split turns one request into two; say so, or the progress line would
	// read "4 / 3" and look like a bug.
	s.planned++
	if err := s.postDepth(ctx, path, first, depth+1, out); err != nil {
		return err
	}
	return s.postDepth(ctx, path, second, depth+1, out)
}

// do is the single HTTP call. The caller passes the encoding that produced the
// bytes — never the target's capability — so the body and the header can never
// disagree.
func (s *transferWireSender) do(ctx context.Context, path, contentType, encoding string, payload []byte) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := s.client.PostEncoded(ctx, path, contentType, encoding, payload, &raw); err != nil {
		return nil, wrapTransferHTTP(err)
	}
	return raw, nil
}

// observe folds a finished request into the link estimate.
//
// The number includes the target's own processing time, because the sender
// cannot see the difference between a slow link and a slow write. That makes it
// an underestimate of the uplink, which is the safe direction: a budget derived
// from it is smaller than it needs to be, never larger.
func (s *transferWireSender) observe(bytes int, elapsed time.Duration) {
	if bytes <= 0 || elapsed <= 0 {
		return
	}
	rate := float64(bytes) / elapsed.Seconds()
	if s.rate <= 0 {
		s.rate = rate
		return
	}
	s.rate = 0.5*s.rate + 0.5*rate
}

// report emits one progress line per request: which request of how many, how
// many wire bytes it carried, and how fast the link has been measured.
func (s *transferWireSender) report(bytes int) {
	s.progress.write(transferProgressEvent{
		Event:                "progress",
		Stage:                s.stage,
		RequestIndex:         s.sent,
		RequestsTotal:        s.planned,
		RequestBytes:         bytes,
		UploadBytesPerSecond: int64(s.rate),
	})
}

// fail turns an exhausted request into either the original error or an
// actionable edge-timeout one.
//
// This message is the deliverable of the whole path. The status that reached a
// user here used to be a 5xx, which the shared classifier renders as "The
// Multica service is temporarily unavailable (server error)" — a sentence that
// sends the reader to check a server that is perfectly healthy, while the real
// constraint is the request's own size against the edge's clock.
func (s *transferWireSender) fail(chunk transferWireChunk, payload []byte, elapsed time.Duration, depth int, err error) error {
	if !transferWireEdgeFailure(err) {
		return err
	}
	rate := s.rate
	if rate <= 0 && elapsed > 0 && len(payload) > 0 {
		rate = float64(len(payload)) / elapsed.Seconds()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "edge_timeout: %s never finished receiving this request. ", s.target.label())
	fmt.Fprintf(&b, "It carried %s compressed and the link has measured %s so far", humanBytes(int64(len(payload))), humanRate(rate))
	if rate > 0 {
		need := time.Duration(float64(len(payload)) / rate * float64(time.Second))
		fmt.Fprintf(&b, ", so a request of this size takes about %s", need.Round(time.Second))
	}
	fmt.Fprintf(&b, ". %s", chunk.Label)
	if depth > 0 {
		fmt.Fprintf(&b, " The transfer already halved this request %d time(s) to reach this size", depth)
		if depth >= service.TransferWireSplitLimit || chunk.Split == nil {
			b.WriteString(", which is the smallest it can send on its own")
		}
	} else if chunk.Split == nil {
		b.WriteString(" This request cannot be split into smaller ones")
	}
	b.WriteString(".")
	b.WriteString("\nNothing already imported is lost: every row is written under a deterministic id, so re-running the same command continues from where it stopped.")
	b.WriteString("\nWhat to do next:")
	b.WriteString("\n  - re-run the same command and let it finish — a slow link simply needs several passes;")
	b.WriteString("\n  - import fewer groups per run (--include config,conversations,attachments) and send the issues group on its own;")
	b.WriteString("\n  - force smaller requests with --max-request-bytes 512KB;")
	b.WriteString("\n  - point the target domain at the origin instead of the CDN, so no edge timer applies;")
	fmt.Fprintf(&b, "\n  - the edge proxy in front of %s drops a request that has not finished in ~100s.", s.target.label())
	return cli.WithUserMessage(b.String(), err)
}

// transferWireEdgeFailure reports whether err is the class of failure a smaller
// request can survive: the edge dropping a request that was still uploading,
// not the target refusing the content.
//
// The list is deliberately narrow. A 400, a 409 or a 401 is a statement about
// the payload, and halving it would only send the same statement twice.
func transferWireEdgeFailure(err error) bool {
	switch httpStatusOf(err) {
	case http.StatusRequestTimeout, http.StatusGatewayTimeout,
		transferStatusEdgeConnectionTimeout, transferStatusEdgeTimeout:
		return true
	}
	var ne *cli.NetworkError
	if errors.As(err, &ne) {
		switch ne.Kind {
		case cli.KindNetworkTimeout, cli.KindNetworkStalled, cli.KindNetworkOffline:
			// An upload that dies mid-body — a reset, an EOF, a deadline —
			// looks exactly like this from the client's side.
			return true
		}
	}
	return false
}

// gzipBytes compresses a request body and reports whether the result is the
// compressed one. The second return value is what the caller must send as
// Content-Encoding: compression is skipped when it does not actually help — an
// incompressible attachment (a zip, a PNG) comes out the same size or larger
// once the gzip header and trailer are counted — and announcing `gzip` over
// those raw bytes makes the target refuse the request outright.
func gzipBytes(raw []byte) ([]byte, bool) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return raw, false
	}
	if _, err := zw.Write(raw); err != nil {
		return raw, false
	}
	if err := zw.Close(); err != nil {
		return raw, false
	}
	if buf.Len() >= len(raw) {
		return raw, false
	}
	return buf.Bytes(), true
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func humanRate(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return "an unknown rate"
	}
	return humanBytes(int64(bytesPerSecond)) + "/s"
}

// transferWireRanges splits [0,n) into consecutive ranges that each fit the
// budget, by halving the ranges that do not.
//
// Halving rather than greedy packing is deliberate: it costs O(log n) size
// measurements over the whole set, which is nothing next to the compression it
// measures, and it produces balanced requests. A range of one item that does
// not fit is returned as-is — only the caller knows whether that item has an
// inside.
func transferWireRanges(n int, fits func(lo, hi int) bool) [][2]int {
	if n <= 0 {
		return nil
	}
	var out [][2]int
	var walk func(lo, hi int)
	walk = func(lo, hi int) {
		if fits(lo, hi) || hi-lo == 1 {
			out = append(out, [2]int{lo, hi})
			return
		}
		mid := lo + (hi-lo)/2
		walk(lo, mid)
		walk(mid, hi)
	}
	walk(0, n)
	return out
}

// transferRowSizer measures JSON rows as they appear in a request body. Rows
// are measured once and summed with a prefix sum, so the cheap "does the raw
// size already fit?" test costs nothing per candidate range.
type transferRowSizer[T any] struct {
	prefix []int
}

func newTransferRowSizer[T any](rows []T) *transferRowSizer[T] {
	s := &transferRowSizer[T]{prefix: make([]int, len(rows)+1)}
	for i := range rows {
		raw, err := json.Marshal(rows[i])
		if err != nil {
			raw = nil
		}
		s.prefix[i+1] = s.prefix[i] + len(raw)
	}
	return s
}

// rawBytes is the summed raw size of rows[lo:hi].
func (s *transferRowSizer[T]) rawBytes(lo, hi int) int {
	if s == nil {
		return 0
	}
	return s.prefix[hi] - s.prefix[lo]
}

// transferRequestFits reports whether one request body fits the budget.
//
// rawRows is the summed raw size of the rows and rawPlus the size of everything
// else the request carries (refs, flags). When even the raw bytes fit there is
// nothing left to check: the send path falls back to the raw bytes when gzip
// does not shrink them, so a body that fits raw can only stay the same size.
//
// compress says whether the target will actually receive the compressed bytes.
// It has to be asked, because the budget is about what travels: planning a
// request by its gzipped size and then sending it raw to an instance that does
// not advertise accepts_gzip puts 5-10x the budget on the wire — which is the
// failure this whole path exists to prevent.
func transferRequestFits(rawRows, rawPlus, limit int, compress bool, build func() []byte) bool {
	if rawRows+rawPlus <= limit {
		return true
	}
	if !compress {
		return false
	}
	wire, _ := gzipBytes(build())
	return len(wire) <= limit
}

// transferIssuesRequest marshals one `/transfer/issues` body.
func transferIssuesRequest(refs service.TransferRefs, dry, renumber, finalize bool, issues []service.TransferIssueRow, comments []service.TransferCommentRow, relations []service.TransferRelationRow) []byte {
	body, err := json.Marshal(service.TransferIssuesRequest{
		Refs:      refs,
		Issues:    issues,
		Comments:  comments,
		Relations: relations,
		DryRun:    boolPtr(dry),
		Renumber:  renumber,
		Finalize:  finalize,
	})
	if err != nil {
		return nil
	}
	return body
}

// transferIssuesChunk is one request's worth of issue rows: the tasks, the
// comments that hang off them, and the relations (labels, reactions) either
// level carries.
type transferIssuesChunk struct {
	Issues    []service.TransferIssueRow
	Comments  []service.TransferCommentRow
	Relations []service.TransferRelationRow
}

// wire turns a planned chunk into the body the sender posts, together with the
// recipe for halving it if the edge drops it.
func (c transferIssuesChunk) wire(refs service.TransferRefs, dry, renumber bool) transferWireChunk {
	out := transferWireChunk{
		Body:        transferIssuesRequest(refs, dry, renumber, false, c.Issues, c.Comments, c.Relations),
		ContentType: transferJSONContentType,
		Label: fmt.Sprintf("It carries %d task row(s), %d comment row(s) and %d relation row(s).",
			len(c.Issues), len(c.Comments), len(c.Relations)),
	}
	out.Split = func() (transferWireChunk, transferWireChunk, bool) {
		first, second, ok := c.split()
		if !ok {
			return transferWireChunk{}, transferWireChunk{}, false
		}
		return first.wire(refs, dry, renumber), second.wire(refs, dry, renumber), true
	}
	return out
}

// split halves a chunk by task, and then by comment for a single task that is
// oversized on its own.
//
// Both levels are safe because the import writes every row under a deterministic
// id with `ON CONFLICT DO NOTHING`: a comment that arrives in a later request
// than its issue, or an issue row repeated because its comments had to be
// split, changes nothing about the result.
func (c transferIssuesChunk) split() (transferIssuesChunk, transferIssuesChunk, bool) {
	if len(c.Issues) >= 2 {
		mid := len(c.Issues) / 2
		first := c.Issues[:mid]
		second := c.Issues[mid:]
		inFirst := map[string]bool{}
		for _, issue := range first {
			inFirst[issue.SourceID] = true
		}
		issueOfComment := map[string]string{}
		for _, comment := range c.Comments {
			issueOfComment[comment.SourceID] = comment.IssueID
		}
		a := transferIssuesChunk{Issues: first}
		b := transferIssuesChunk{Issues: second}
		for _, comment := range c.Comments {
			if inFirst[issueOfComment[comment.SourceID]] {
				a.Comments = append(a.Comments, comment)
			} else {
				b.Comments = append(b.Comments, comment)
			}
		}
		for _, rel := range c.Relations {
			key := rel.IssueID
			if rel.CommentID != "" {
				key = issueOfComment[rel.CommentID]
			}
			if inFirst[key] {
				a.Relations = append(a.Relations, rel)
			} else {
				b.Relations = append(b.Relations, rel)
			}
		}
		return a, b, true
	}
	if len(c.Comments) >= 2 {
		mid := len(c.Comments) / 2
		first := c.Comments[:mid]
		inFirst := map[string]bool{}
		for _, comment := range first {
			inFirst[comment.SourceID] = true
		}
		a := transferIssuesChunk{Issues: c.Issues, Comments: first}
		b := transferIssuesChunk{Issues: c.Issues, Comments: c.Comments[mid:]}
		for _, rel := range c.Relations {
			if rel.CommentID != "" && inFirst[rel.CommentID] {
				a.Relations = append(a.Relations, rel)
			} else {
				b.Relations = append(b.Relations, rel)
			}
		}
		return a, b, true
	}
	return transferIssuesChunk{}, transferIssuesChunk{}, false
}

// transferIssuesPlanner packs one issues shard into requests that fit the
// budget.
//
// Comments and relations travel with the issues they belong to, because that
// pairing is the exporter's own contract (`CommentShards[i]` holds exactly the
// comments of `IssueShards[i]`).
type transferIssuesPlanner struct {
	limit int
	// compress mirrors the sender's decision, so the planner measures the
	// bytes that will actually travel rather than the ones it could make.
	compress         bool
	refs             service.TransferRefs
	dry              bool
	renumber         bool
	issues           []service.TransferIssueRow
	commentsByIssue  map[string][]service.TransferCommentRow
	relationsByIssue map[string][]service.TransferRelationRow
	overhead         int
	// rawPrefix sums, per issue, the raw JSON size of the issue with the
	// comments and relations that travel with it. It is what makes the cheap
	// "already fits raw" test honest: a request's size is not the size of its
	// task rows alone.
	rawPrefix []int
}

func newTransferIssuesPlanner(limit int, compress bool, refs service.TransferRefs, dry, renumber bool, issues []service.TransferIssueRow, comments []service.TransferCommentRow, relations []service.TransferRelationRow) *transferIssuesPlanner {
	p := &transferIssuesPlanner{
		limit:            limit,
		compress:         compress,
		refs:             refs,
		dry:              dry,
		renumber:         renumber,
		issues:           issues,
		commentsByIssue:  map[string][]service.TransferCommentRow{},
		relationsByIssue: map[string][]service.TransferRelationRow{},
	}
	commentIssue := map[string]string{}
	for _, c := range comments {
		p.commentsByIssue[c.IssueID] = append(p.commentsByIssue[c.IssueID], c)
		commentIssue[c.SourceID] = c.IssueID
	}
	for _, rel := range relations {
		key := rel.IssueID
		if rel.CommentID != "" {
			if issueID, ok := commentIssue[rel.CommentID]; ok {
				key = issueID
			}
		}
		p.relationsByIssue[key] = append(p.relationsByIssue[key], rel)
	}
	p.overhead = len(transferIssuesRequest(refs, dry, renumber, false, nil, nil, nil))
	p.rawPrefix = make([]int, len(issues)+1)
	for i, issue := range issues {
		p.rawPrefix[i+1] = p.rawPrefix[i] + rawJSONSize(issue) +
			rawJSONSize(p.commentsByIssue[issue.SourceID]) + rawJSONSize(p.relationsByIssue[issue.SourceID])
	}
	return p
}

// rawJSONSize is the JSON size of one value, and of a slice of them. A value
// that cannot be marshalled counts as zero, which only ever makes the cheap
// test more conservative.
func rawJSONSize(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(raw)
}

func (p *transferIssuesPlanner) commentsOf(lo, hi int) []service.TransferCommentRow {
	var out []service.TransferCommentRow
	for _, issue := range p.issues[lo:hi] {
		out = append(out, p.commentsByIssue[issue.SourceID]...)
	}
	return out
}

func (p *transferIssuesPlanner) relationsOf(lo, hi int) []service.TransferRelationRow {
	var out []service.TransferRelationRow
	for _, issue := range p.issues[lo:hi] {
		out = append(out, p.relationsByIssue[issue.SourceID]...)
	}
	return out
}

// fits reports whether issues[lo:hi], with the comments and relations that hang
// off them, produce a request inside the budget.
func (p *transferIssuesPlanner) fits(lo, hi int) bool {
	return transferRequestFits(p.rawPrefix[hi]-p.rawPrefix[lo], p.overhead, p.limit, p.compress, func() []byte {
		return transferIssuesRequest(p.refs, p.dry, p.renumber, false, p.issues[lo:hi], p.commentsOf(lo, hi), p.relationsOf(lo, hi))
	})
}

// plan returns every request the shard needs, in order.
func (p *transferIssuesPlanner) plan() []transferIssuesChunk {
	var out []transferIssuesChunk
	for _, r := range transferWireRanges(len(p.issues), p.fits) {
		lo, hi := r[0], r[1]
		if hi-lo == 1 && !p.fits(lo, hi) {
			out = append(out, p.splitSingleIssue(lo)...)
			continue
		}
		out = append(out, transferIssuesChunk{
			Issues:    p.issues[lo:hi],
			Comments:  p.commentsOf(lo, hi),
			Relations: p.relationsOf(lo, hi),
		})
	}
	return out
}

// splitSingleIssue divides one oversized issue's comments into requests that
// fit. The issue row is repeated in every piece; its own relations (labels,
// reactions) ride in the first one.
func (p *transferIssuesPlanner) splitSingleIssue(index int) []transferIssuesChunk {
	issue := p.issues[index]
	comments := p.commentsByIssue[issue.SourceID]
	relations := p.relationsByIssue[issue.SourceID]
	if len(comments) == 0 {
		return []transferIssuesChunk{{Issues: []service.TransferIssueRow{issue}, Relations: relations}}
	}
	issueRelations := make([]service.TransferRelationRow, 0, len(relations))
	for _, rel := range relations {
		if rel.CommentID == "" {
			issueRelations = append(issueRelations, rel)
		}
	}
	commentRelations := func(lo, hi int) []service.TransferRelationRow {
		wanted := map[string]bool{}
		for _, c := range comments[lo:hi] {
			wanted[c.SourceID] = true
		}
		var out []service.TransferRelationRow
		for _, rel := range relations {
			if rel.CommentID != "" && wanted[rel.CommentID] {
				out = append(out, rel)
			}
		}
		return out
	}
	sizer := newTransferRowSizer(comments)
	fits := func(lo, hi int) bool {
		return transferRequestFits(sizer.rawBytes(lo, hi), p.overhead+rawJSONSize(issue), p.limit, p.compress, func() []byte {
			return transferIssuesRequest(p.refs, p.dry, p.renumber, false,
				[]service.TransferIssueRow{issue}, comments[lo:hi], commentRelations(lo, hi))
		})
	}
	var out []transferIssuesChunk
	for i, r := range transferWireRanges(len(comments), fits) {
		lo, hi := r[0], r[1]
		chunk := transferIssuesChunk{Issues: []service.TransferIssueRow{issue}, Comments: comments[lo:hi]}
		chunk.Relations = commentRelations(lo, hi)
		if i == 0 {
			chunk.Relations = append(chunk.Relations, issueRelations...)
		}
		out = append(out, chunk)
	}
	return out
}

// transferConversationsRequest marshals one `/transfer/conversations` body.
func transferConversationsRequest(refs service.TransferRefs, dry, finalize bool, sessions []service.TransferSessionRow, messages []service.TransferMessageRow) []byte {
	body, err := json.Marshal(service.TransferConversationsRequest{
		Refs:     refs,
		Sessions: sessions,
		Messages: messages,
		DryRun:   boolPtr(dry),
		Finalize: finalize,
	})
	if err != nil {
		return nil
	}
	return body
}

// transferConversationsChunk is one request's worth of chat rows.
type transferConversationsChunk struct {
	Sessions []service.TransferSessionRow
	Messages []service.TransferMessageRow
}

func (c transferConversationsChunk) wire(refs service.TransferRefs, dry, finalize bool) transferWireChunk {
	out := transferWireChunk{
		Body:        transferConversationsRequest(refs, dry, finalize, c.Sessions, c.Messages),
		ContentType: transferJSONContentType,
		Label: fmt.Sprintf("It carries %d chat session row(s) and %d message row(s).",
			len(c.Sessions), len(c.Messages)),
	}
	if finalize {
		// The finalize pass is not repeatable — it moves the workspace's issue
		// watermark and rebuilds subscribers — so it is never halved.
		out.Label += " It is the finalize pass and cannot be split."
		return out
	}
	out.Split = func() (transferWireChunk, transferWireChunk, bool) {
		first, second, ok := c.split()
		if !ok {
			return transferWireChunk{}, transferWireChunk{}, false
		}
		return first.wire(refs, dry, false), second.wire(refs, dry, false), true
	}
	return out
}

// split halves a chunk by session, then by message for a single long session.
func (c transferConversationsChunk) split() (transferConversationsChunk, transferConversationsChunk, bool) {
	if len(c.Sessions) >= 2 {
		mid := len(c.Sessions) / 2
		first := c.Sessions[:mid]
		inFirst := map[string]bool{}
		for _, session := range first {
			inFirst[session.SourceID] = true
		}
		a := transferConversationsChunk{Sessions: first}
		b := transferConversationsChunk{Sessions: c.Sessions[mid:]}
		for _, message := range c.Messages {
			if inFirst[message.ChatSessionID] {
				a.Messages = append(a.Messages, message)
			} else {
				b.Messages = append(b.Messages, message)
			}
		}
		return a, b, true
	}
	if len(c.Messages) >= 2 {
		mid := len(c.Messages) / 2
		return transferConversationsChunk{Sessions: c.Sessions, Messages: c.Messages[:mid]},
			transferConversationsChunk{Sessions: c.Sessions, Messages: c.Messages[mid:]}, true
	}
	return transferConversationsChunk{}, transferConversationsChunk{}, false
}

// transferConversationsPlanner packs one conversations shard into requests that
// fit the budget. Messages travel with their session for the same reason
// comments travel with their issue.
type transferConversationsPlanner struct {
	limit          int
	compress       bool
	refs           service.TransferRefs
	dry            bool
	sessions       []service.TransferSessionRow
	messagesByChat map[string][]service.TransferMessageRow
	overhead       int
	// rawPrefix sums, per session, the raw JSON size of the session with the
	// messages that travel with it.
	rawPrefix []int
}

func newTransferConversationsPlanner(limit int, compress bool, refs service.TransferRefs, dry bool, sessions []service.TransferSessionRow, messages []service.TransferMessageRow) *transferConversationsPlanner {
	p := &transferConversationsPlanner{
		limit:          limit,
		compress:       compress,
		refs:           refs,
		dry:            dry,
		sessions:       sessions,
		messagesByChat: map[string][]service.TransferMessageRow{},
	}
	for _, m := range messages {
		p.messagesByChat[m.ChatSessionID] = append(p.messagesByChat[m.ChatSessionID], m)
	}
	p.overhead = len(transferConversationsRequest(refs, dry, false, nil, nil))
	p.rawPrefix = make([]int, len(sessions)+1)
	for i, session := range sessions {
		p.rawPrefix[i+1] = p.rawPrefix[i] + rawJSONSize(session) + rawJSONSize(p.messagesByChat[session.SourceID])
	}
	return p
}

func (p *transferConversationsPlanner) messagesOf(lo, hi int) []service.TransferMessageRow {
	var out []service.TransferMessageRow
	for _, session := range p.sessions[lo:hi] {
		out = append(out, p.messagesByChat[session.SourceID]...)
	}
	return out
}

func (p *transferConversationsPlanner) fits(lo, hi int) bool {
	return transferRequestFits(p.rawPrefix[hi]-p.rawPrefix[lo], p.overhead, p.limit, p.compress, func() []byte {
		return transferConversationsRequest(p.refs, p.dry, false, p.sessions[lo:hi], p.messagesOf(lo, hi))
	})
}

func (p *transferConversationsPlanner) plan() []transferConversationsChunk {
	var out []transferConversationsChunk
	for _, r := range transferWireRanges(len(p.sessions), p.fits) {
		lo, hi := r[0], r[1]
		if hi-lo == 1 && !p.fits(lo, hi) {
			out = append(out, p.splitSingleSession(lo)...)
			continue
		}
		out = append(out, transferConversationsChunk{Sessions: p.sessions[lo:hi], Messages: p.messagesOf(lo, hi)})
	}
	return out
}

func (p *transferConversationsPlanner) splitSingleSession(index int) []transferConversationsChunk {
	session := p.sessions[index]
	messages := p.messagesByChat[session.SourceID]
	if len(messages) == 0 {
		return []transferConversationsChunk{{Sessions: []service.TransferSessionRow{session}}}
	}
	sizer := newTransferRowSizer(messages)
	fits := func(lo, hi int) bool {
		return transferRequestFits(sizer.rawBytes(lo, hi), p.overhead+rawJSONSize(session), p.limit, p.compress, func() []byte {
			return transferConversationsRequest(p.refs, p.dry, false, []service.TransferSessionRow{session}, messages[lo:hi])
		})
	}
	var out []transferConversationsChunk
	for _, r := range transferWireRanges(len(messages), fits) {
		out = append(out, transferConversationsChunk{
			Sessions: []service.TransferSessionRow{session},
			Messages: messages[r[0]:r[1]],
		})
	}
	return out
}
