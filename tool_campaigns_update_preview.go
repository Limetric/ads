package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// campaignTargetNames enriches a preview without changing the staged criteria.
// IDs have already been validated. Missing or unreadable constants remain
// explicitly unidentified in the preview rather than hiding their IDs.
func campaignTargetNames(ctx context.Context, c *Client, customerID string, ids []string, geo bool) map[string]string {
	names := make(map[string]string)
	if len(ids) == 0 {
		return names
	}
	table, key := "language_constant", "languageConstant"
	if geo {
		table, key = "geo_target_constant", "geoTargetConstant"
	}
	fields := table + ".id, " + table + ".name"
	if geo {
		fields += ", " + table + ".canonical_name"
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s.id IN (%s)", fields, table, table, strings.Join(ids, ", "))
	rows, err := c.Search(ctx, customerID, query)
	if err != nil {
		return names
	}
	for _, raw := range rows {
		var row map[string]json.RawMessage
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		var constant struct {
			ID            json.Number `json:"id"`
			Name          string      `json:"name"`
			CanonicalName string      `json:"canonicalName"`
		}
		if json.Unmarshal(row[key], &constant) != nil {
			continue
		}
		name := constant.Name
		if constant.CanonicalName != "" {
			name = constant.CanonicalName
		}
		names[string(constant.ID)] = name
	}
	return names
}

func campaignTargetLabel(id string, names map[string]string) string {
	if name := names[id]; name != "" {
		return fmt.Sprintf("%q (%s)", name, id)
	}
	return fmt.Sprintf("%s (name unavailable)", id)
}
