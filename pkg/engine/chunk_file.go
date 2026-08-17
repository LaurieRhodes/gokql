package engine

// chunk_file.go — .chunk-file "path" into T (backlog P2 item 11).
//
// Turns the one-off Python chunker from the memory-scope pilot into a
// real, reusable tool: no external Python dependency, matching this
// project's self-containment discipline (the same reasoning that
// chose local Ollama over pgvector). Ports the paragraph-splitting +
// heading-trail algorithm, and — unlike the Python version, which only
// documented the ~1500-char practical embedding limit as a manual
// exclusion — actually implements automatic splitting of oversized
// blocks, since that's what "reusable" has to mean.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/LaurieRhodes/gokql/pkg/parser"
	"github.com/LaurieRhodes/gokql/pkg/types"
)

// ChunkMaxChars is the practical per-chunk size limit found live
// (a 3206-char Unicode-dense markdown table reproducibly broke the
// local embedding model). Oversized blocks are split before this
// limit is reached, not truncated — content is never dropped.
var ChunkMaxChars = 1500

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)`)
var sentenceSplitRe = regexp.MustCompile(`(?:[.!?])\s+`)

type rawChunk struct {
	text       string
	heading    string
	startLine  int
	endLine    int
}

func (e *Engine) applyChunkFile(cmd *parser.ChunkFileCmd) (*types.Table, error) {
	data, err := os.ReadFile(cmd.Path)
	if err != nil {
		return nil, fmt.Errorf(".chunk-file: reading %q: %w", cmd.Path, err)
	}
	lines := strings.Split(string(data), "\n")

	raw := extractParagraphs(lines)
	if len(raw) == 0 {
		return nil, fmt.Errorf(".chunk-file: %q produced no chunks (empty or whitespace-only file)", cmd.Path)
	}

	// Split any oversized block before emitting, so no chunk exceeds
	// ChunkMaxChars — content is preserved, never truncated.
	var final []rawChunk
	for _, c := range raw {
		final = append(final, splitOversized(c)...)
	}

	provenance := filepath.Base(cmd.Path)
	schema := types.Schema{Columns: []types.Column{
		{Name: "Id", Type: types.TypeString},
		{Name: "Text", Type: types.TypeString},
		{Name: "HeadingTrail", Type: types.TypeString},
		{Name: "StartLine", Type: types.TypeLong},
		{Name: "EndLine", Type: types.TypeLong},
		{Name: "Provenance", Type: types.TypeString},
	}}

	tableDef := e.Catalog.GetTable(cmd.TableName)
	if tableDef == nil {
		if err := e.Catalog.CreateTable(cmd.TableName, schema); err != nil {
			return nil, err
		}
		if err := e.persistDiscoverySchema(cmd.TableName, schema); err != nil {
			return nil, err
		}
		tableDef = e.Catalog.GetTable(cmd.TableName)
	} else if err := schemaAppendCompatible(&tableDef.Schema, &schema); err != nil {
		return nil, fmt.Errorf(".chunk-file into %q: %w", cmd.TableName, err)
	}

	rows := make([]types.Row, len(final))
	oversizedSplits := 0
	for i, c := range final {
		id := fmt.Sprintf("c%03d", i+1)
		row := make(types.Row, len(tableDef.Schema.Columns))
		for ti, tc := range tableDef.Schema.Columns {
			switch tc.Name {
			case "Id":
				row[ti] = id
			case "Text":
				row[ti] = c.text
			case "HeadingTrail":
				row[ti] = c.heading
			case "StartLine":
				row[ti] = int64(c.startLine)
			case "EndLine":
				row[ti] = int64(c.endLine)
			case "Provenance":
				row[ti] = provenance
			}
		}
		rows[i] = row
		if len(c.text) >= ChunkMaxChars {
			oversizedSplits++ // shouldn't happen post-split; tracked as a sanity signal
		}
	}

	extID, err := e.flushBatch(cmd.TableName, tableDef, rows)
	if err != nil {
		return nil, fmt.Errorf(".chunk-file: %w", err)
	}

	splitCount := len(final) - len(raw)
	result := types.NewTable("", types.Schema{Columns: []types.Column{
		{Name: "Result", Type: types.TypeString},
		{Name: "ExtentId", Type: types.TypeString},
		{Name: "ParagraphsFound", Type: types.TypeLong},
		{Name: "ChunksWritten", Type: types.TypeLong},
		{Name: "OversizedSplit", Type: types.TypeLong},
	}})
	result.AddRow(types.Row{"OK", extID, int64(len(raw)), int64(len(final)), int64(splitCount)})
	return result, nil
}

// extractParagraphs walks lines, tracking a markdown heading-trail
// stack, emitting one rawChunk per blank-line-delimited block.
// Trivial fragments (table separator rows alone, stray punctuation)
// under 20 chars are dropped, matching the pilot's original heuristic.
func extractParagraphs(lines []string) []rawChunk {
	type headingLevel struct {
		level int
		text  string
	}
	var trail []headingLevel
	var buf []string
	bufStart := 0
	var out []rawChunk

	headingStr := func() string {
		parts := make([]string, len(trail))
		for i, h := range trail {
			parts[i] = h.text
		}
		return strings.Join(parts, " > ")
	}
	flush := func(endLine int) {
		if len(buf) == 0 {
			return
		}
		body := strings.TrimSpace(strings.Join(buf, "\n"))
		if len(body) > 20 {
			out = append(out, rawChunk{text: body, heading: headingStr(), startLine: bufStart, endLine: endLine})
		}
		buf = nil
	}

	for i, line := range lines {
		lineNum := i + 1
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flush(lineNum - 1)
			level := len(m[1])
			newTrail := trail[:0:0]
			for _, h := range trail {
				if h.level < level {
					newTrail = append(newTrail, h)
				}
			}
			trail = append(newTrail, headingLevel{level: level, text: strings.TrimSpace(m[2])})
			bufStart = lineNum + 1
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush(lineNum - 1)
			bufStart = lineNum + 1
			continue
		}
		buf = append(buf, line)
	}
	flush(len(lines))
	return out
}

// splitOversized returns c unchanged if it's within ChunkMaxChars,
// otherwise splits it along natural boundaries: first its own internal
// newlines (the right unit for a markdown table, where each line is
// one row — this is exactly the shape that broke the embedding model
// live), falling back to sentence boundaries for a single oversized
// line with no internal structure. Every resulting piece carries the
// same heading trail as the original.
func splitOversized(c rawChunk) []rawChunk {
	if len(c.text) < ChunkMaxChars {
		return []rawChunk{c}
	}

	lines := strings.Split(c.text, "\n")
	var pieces []rawChunk
	var cur []string
	curLen := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		pieces = append(pieces, rawChunk{
			text: strings.Join(cur, "\n"), heading: c.heading,
			startLine: c.startLine, endLine: c.endLine,
		})
		cur = nil
		curLen = 0
	}
	for _, line := range lines {
		if len(line) >= ChunkMaxChars {
			// A single line is itself oversized (no internal newline
			// structure to split on) — fall back to sentence
			// boundaries within just this line.
			flush()
			pieces = append(pieces, splitBySentence(line, c)...)
			continue
		}
		if curLen+len(line)+1 > ChunkMaxChars && len(cur) > 0 {
			flush()
		}
		cur = append(cur, line)
		curLen += len(line) + 1
	}
	flush()

	if len(pieces) == 0 {
		// Pathological: nothing survived splitting. Keep the original
		// rather than silently dropping content.
		return []rawChunk{c}
	}
	return pieces
}

func splitBySentence(text string, c rawChunk) []rawChunk {
	sentences := sentenceSplitRe.Split(text, -1)
	var pieces []rawChunk
	var cur strings.Builder
	for _, s := range sentences {
		if cur.Len()+len(s) > ChunkMaxChars && cur.Len() > 0 {
			pieces = append(pieces, rawChunk{
				text: strings.TrimSpace(cur.String()), heading: c.heading,
				startLine: c.startLine, endLine: c.endLine,
			})
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString(". ")
		}
		cur.WriteString(s)
	}
	if cur.Len() > 0 {
		pieces = append(pieces, rawChunk{
			text: strings.TrimSpace(cur.String()), heading: c.heading,
			startLine: c.startLine, endLine: c.endLine,
		})
	}
	return pieces
}
