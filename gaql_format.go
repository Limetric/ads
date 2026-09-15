package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file provides GAQL result-shaping helpers: SELECT-field extraction,
// micros→human cost enrichment, table/CSV rendering, a date WHERE-clause
// builder, and GAQL error hints. They operate on the
// []json.RawMessage rows returned by Client.Search.

// fromKeywordRe locates the FROM clause keyword as a standalone token, so
// field names that merely contain "from" (e.g.
// metrics.conversions_from_interactions_rate) don't truncate the SELECT list.
var fromKeywordRe = regexp.MustCompile(`(?i)\bFROM\b`)

// parseSelectFields extracts the field names from a GAQL query's SELECT clause.
//
//	"SELECT campaign.id, campaign.name FROM campaign" -> ["campaign.id", "campaign.name"]
func parseSelectFields(query string) []string {
	upper := strings.ToUpper(query)
	sel := strings.Index(upper, "SELECT")
	if sel < 0 {
		return nil
	}
	start := sel + len("SELECT")
	end := len(query)
	if loc := fromKeywordRe.FindStringIndex(query[start:]); loc != nil {
		end = start + loc[0]
	}
	var fields []string
	for _, f := range strings.Split(query[start:end], ",") {
		if f = strings.TrimSpace(f); f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

// dateClause builds a GAQL date WHERE fragment over segments.date.
//
//	dateClause("2024-01-01", "2024-01-31")
//	  -> "segments.date BETWEEN '2024-01-01' AND '2024-01-31'"
func dateClause(start, end string) string {
	return fmt.Sprintf("segments.date BETWEEN '%s' AND '%s'", start, end)
}

// validGAQLDate reports whether s is a YYYY-MM-DD date literal. Date args are
// caller-supplied and interpolated into GAQL, so anything else is rejected
// before it reaches a query (issue #13).
func validGAQLDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, r := range s {
		if i == 4 || i == 7 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dateRangeClause builds the segments.date WHERE fragment shared by the
// metrics read tools: an explicit validated range when both dates are set,
// LAST_30_DAYS when neither is. A single-ended range errors — it used to be
// silently ignored, returning data for a window the user didn't ask for
// (issue #13).
func dateRangeClause(start, end string) (string, error) {
	switch {
	case start == "" && end == "":
		return "segments.date DURING LAST_30_DAYS", nil
	case start == "" || end == "":
		return "", fmt.Errorf("date_start and date_end must both be set (got start=%q, end=%q)", start, end)
	}
	if !validGAQLDate(start) || !validGAQLDate(end) {
		return "", fmt.Errorf("dates must be YYYY-MM-DD (got start=%q, end=%q)", start, end)
	}
	return dateClause(start, end), nil
}

// decodeRow decodes a raw result row into a generic value, keeping numbers as
// json.Number so re-marshaling preserves their original representation (large
// IDs never get rewritten in scientific notation).
func decodeRow(r json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(r))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

// enrichCostFields walks each row and, for every key ending in "_micros" or
// "Micros", inserts a sibling "<base>_readable" string with the value divided
// by 1,000,000 and formatted to two decimals. Rows that fail to decode are
// passed through unchanged.
func enrichCostFields(rows []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		v, ok := decodeRow(r)
		if !ok {
			out[i] = r
			continue
		}
		enrichCostRecursive(v)
		nb, err := json.Marshal(v)
		if err != nil {
			out[i] = r
			continue
		}
		out[i] = nb
	}
	return out
}

func enrichCostRecursive(value any) {
	switch t := value.(type) {
	case map[string]any:
		additions := make(map[string]any)
		for key, val := range t {
			if !strings.HasSuffix(key, "_micros") && !strings.HasSuffix(key, "Micros") {
				continue
			}
			if micros, ok := asFloat(val); ok {
				humanKey := strings.ReplaceAll(strings.ReplaceAll(key, "_micros", ""), "Micros", "")
				additions[humanKey+"_readable"] = fmt.Sprintf("%.2f", micros/1_000_000.0)
			}
		}
		for k, v := range additions {
			t[k] = v
		}
		for _, val := range t {
			enrichCostRecursive(val)
		}
	case []any:
		for _, item := range t {
			enrichCostRecursive(item)
		}
	}
}

// asFloat coerces a JSON number or numeric string to float64.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// snakeToCamel converts a GAQL path segment to the camelCase key the REST API
// uses in result rows ("cost_micros" -> "costMicros", "ad_group" -> "adGroup").
func snakeToCamel(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	parts := strings.Split(s, "_")
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// resolveField resolves a dotted field path inside a decoded row, returning a
// flat string cell. Fields are GAQL snake_case paths while REST rows carry
// camelCase keys, so each segment is tried verbatim and camelCased. Missing
// paths resolve to "".
func resolveField(row any, field string) string {
	cur := row
	for _, part := range strings.Split(field, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		v, ok := m[part]
		if !ok {
			if v, ok = m[snakeToCamel(part)]; !ok {
				return ""
			}
		}
		cur = v
	}
	switch v := cur.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// formatTable renders rows as an aligned, pipe-separated text table over the
// given fields. Returns "No results found." for an empty row set.
func formatTable(rows []json.RawMessage, fields []string) string {
	if len(rows) == 0 {
		return "No results found."
	}
	widths := make([]int, len(fields))
	for i, f := range fields {
		widths[i] = displayWidth(f)
	}
	cells := make([][]string, len(rows))
	for ri, r := range rows {
		v, _ := decodeRow(r)
		row := make([]string, len(fields))
		for ci, f := range fields {
			val := resolveField(v, f)
			row[ci] = val
			if w := displayWidth(val); w > widths[ci] {
				widths[ci] = w
			}
		}
		cells[ri] = row
	}

	var b strings.Builder
	header := make([]string, len(fields))
	for i, f := range fields {
		header[i] = padRight(f, widths[i])
	}
	b.WriteString(strings.Join(header, " | "))
	b.WriteByte('\n')

	sep := make([]string, len(widths))
	for i, w := range widths {
		sep[i] = strings.Repeat("-", w)
	}
	b.WriteString(strings.Join(sep, "-+-"))
	b.WriteByte('\n')

	for _, row := range cells {
		out := make([]string, len(row))
		for i, c := range row {
			out[i] = padRight(c, widths[i])
		}
		b.WriteString(strings.Join(out, " | "))
		b.WriteByte('\n')
	}
	return b.String()
}

// displayWidth measures a cell in runes, not bytes, so non-ASCII campaign
// names don't skew column padding (issue #17). Double-width (CJK) runes still
// count as one column — a known approximation that avoids a width-table
// dependency.
func displayWidth(s string) int {
	return utf8.RuneCountInString(s)
}

func padRight(s string, w int) string {
	if d := displayWidth(s); d < w {
		return s + strings.Repeat(" ", w-d)
	}
	return s
}

// formatCSV renders rows as CSV over the given fields, RFC-4180 quoting cells
// that contain commas, quotes, or newlines.
func formatCSV(rows []json.RawMessage, fields []string) string {
	var b strings.Builder
	b.WriteString(strings.Join(fields, ","))
	b.WriteByte('\n')
	for _, r := range rows {
		v, _ := decodeRow(r)
		vals := make([]string, len(fields))
		for i, f := range fields {
			val := resolveField(v, f)
			if strings.ContainsAny(val, ",\"\n") {
				val = `"` + strings.ReplaceAll(val, `"`, `""`) + `"`
			}
			vals[i] = val
		}
		b.WriteString(strings.Join(vals, ","))
		b.WriteByte('\n')
	}
	return b.String()
}

// errorHints maps Google Ads error substrings to actionable suggestions.
var errorHints = []struct{ key, hint string }{
	{"UNRECOGNIZED_FIELD", "The field name is not recognized. Check the Google Ads API field reference."},
	{"INVALID_FIELD_IN_SELECT", "This field cannot be used in SELECT with the given FROM resource."},
	{"INVALID_FIELD_IN_WHERE", "This field cannot be used in WHERE clause with the given FROM resource."},
	{"INVALID_FIELD_IN_ORDER_BY", "This field cannot be used in ORDER BY with the given FROM resource."},
	{"PROHIBITED_RESOURCE_TYPE_IN_FROM_CLAUSE", "This resource type cannot be used in FROM clause directly."},
	{"PROHIBITED_METRIC_IN_SELECT_OR_WHERE_CLAUSE", "This metric cannot be used with the selected date range or segmentation."},
	{"PROHIBITED_SEGMENT_IN_SELECT_OR_WHERE_CLAUSE", "This segment conflicts with other selected fields."},
	{"MUTUALLY_EXCLUSIVE_FIELDS", "Two or more selected fields cannot be used together."},
	{"DATE_RANGE_TOO_WIDE", "The date range is too wide for the requested metrics. Try narrowing the date range."},
	{"AUTHORIZATION_ERROR", "Check your customer ID and login customer ID, and that the Google Cloud project behind your OAuth client has Google Ads API access."},
}

// getErrorHint returns a human-readable hint if the error message contains a
// known Google Ads error token, or "" if none match.
func getErrorHint(errorMessage string) string {
	for _, e := range errorHints {
		if strings.Contains(errorMessage, e.key) {
			return e.hint
		}
	}
	return ""
}
