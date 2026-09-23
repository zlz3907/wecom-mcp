package mcp

import (
	"context"
	"fmt"
	"regexp"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/config"
	"github.com/zhonglizhi/wecom-mcp-v2/internal/wecom"
)

var discoveredIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// ResolveDiscoveredInstanceName reads an existing, complete active Z-S00 through
// the fixed Source and Registry. It neither provisions assets nor resolves an
// execution identity. InstanceName in the input is only a validation placeholder.
func ResolveDiscoveredInstanceName(ctx context.Context, runtime config.Config) (string, error) {
	if err := runtime.Validate(); err != nil {
		return "", fmt.Errorf("discovery runtime is invalid")
	}
	client, err := wecom.NewFromEnvironment(runtime.TenantRoute)
	if err != nil {
		return "", fmt.Errorf("discovery managed client is unavailable")
	}
	return resolveDiscoveredInstanceName(ctx, runtime, client)
}

func resolveDiscoveredInstanceName(ctx context.Context, runtime config.Config, client wecom.Requester) (string, error) {
	if err := runtime.Validate(); err != nil {
		return "", fmt.Errorf("discovery runtime is invalid")
	}
	reader := &discoveryReader{client: client, allowed: runtime.Allows, cache: map[string]map[string]any{}, records: map[string][]any{}}
	// ResolveTarget selects a single Registry sheet; reject ambiguity before
	// calling it, and fully paginate its records through discoveryReader.
	sheets, err := reader.Request(ctx, "get_sheet", map[string]any{"docid": runtime.RegistryDocumentID})
	if err != nil {
		return "", err
	}
	count := 0
	for _, raw := range resultSlice(sheets, "sheet_list") {
		sheet, _ := raw.(map[string]any)
		if sheet["type"] == "smartsheet" {
			count++
		}
	}
	if count != 1 {
		return "", fmt.Errorf("discovery Registry sheet is not unique")
	}
	anchor, err := wecom.ResolveTarget(ctx, reader, runtime.RegistryDocumentID, runtime.RegistryKey, "Z-S01", runtime.Allows)
	if err != nil {
		return "", fmt.Errorf("discovery active Registry target is unavailable")
	}
	sheetID, err := resolveExactSheetIDWithRequester(ctx, reader, anchor.DocumentID, schemaRegistrySheetTitle)
	if err != nil {
		return "", fmt.Errorf("discovery Z-S00 is unavailable")
	}
	fields, err := wecom.ReadFields(ctx, reader, wecom.Target{DocumentID: anchor.DocumentID, SheetID: sheetID}, runtime.Allows)
	if err != nil {
		return "", err
	}
	ids := map[string]string{}
	for _, field := range fields {
		title, _ := field["field_title"].(string)
		id, _ := field["field_id"].(string)
		ids[title] = id
	}
	if ids["Schema 条目键"] == "" || ids["实例名称"] == "" {
		return "", fmt.Errorf("discovery Z-S00 identity fields are missing")
	}
	records, complete, err := readAllInitializeRecords(ctx, reader, anchor.DocumentID, sheetID)
	if err != nil || !complete {
		return "", fmt.Errorf("discovery Z-S00 records are incomplete")
	}
	name, matches := "", 0
	for _, raw := range records {
		record, _ := raw.(map[string]any)
		values, _ := record["values"].(map[string]any)
		if initializeTextCell(values[ids["Schema 条目键"]]) == schemaRegistryActiveKey {
			matches++
			name = initializeTextCell(values[ids["实例名称"]])
		}
	}
	if matches != 1 || !discoveredIdentifier.MatchString(name) {
		return "", fmt.Errorf("discovery active instance name is invalid or ambiguous")
	}
	runtime.InstanceName = name
	table, err := loadSchemaRegistryTable(ctx, runtime, reader)
	if err != nil {
		return "", fmt.Errorf("discovery active Schema identity is invalid")
	}
	if _, err := runtimeSchemaFromRegistryTable(table, runtime); err != nil {
		return "", fmt.Errorf("discovery active Schema is incomplete or inconsistent")
	}
	return name, nil
}

// A reader is local to one discovery. It retains complete record sets so both
// initial identity discovery and existing Schema validation see the same data.
// The underlying requester can only receive the three permitted read methods.
type discoveryReader struct {
	client  wecom.Requester
	allowed func(string) bool
	cache   map[string]map[string]any
	records map[string][]any
}

func (r *discoveryReader) Request(ctx context.Context, operation string, payload any) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if (operation != "get_sheet" && operation != "get_fields" && operation != "get_records") || !r.allowed(operation) {
		return nil, fmt.Errorf("discovery operation is not allowed")
	}
	input, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("discovery request is invalid")
	}
	doc, _ := input["docid"].(string)
	sheet, _ := input["sheet_id"].(string)
	if !discoveredIdentifier.MatchString(doc) || operation != "get_sheet" && !discoveredIdentifier.MatchString(sheet) {
		return nil, fmt.Errorf("discovery target identifier is invalid")
	}
	key := operation + "\x00" + doc + "\x00" + sheet
	if operation == "get_records" {
		records, exists := r.records[key]
		if !exists {
			var complete bool
			var err error
			records, complete, err = readAllInitializeRecords(ctx, r.client, doc, sheet)
			if err != nil || !complete {
				return nil, fmt.Errorf("discovery records are incomplete")
			}
			r.records[key] = records
		}
		// ResolveTarget does not request an offset; give it the entire Registry
		// so duplicate active entries beyond its historic limit are visible.
		if offset, paged := input["offset"].(int); paged {
			limit, _ := input["limit"].(int)
			if offset < 0 || limit <= 0 {
				return nil, fmt.Errorf("discovery pagination is invalid")
			}
			start := min(offset, len(records))
			end := start + min(limit, len(records)-start)
			return map[string]any{"result": map[string]any{"records": records[start:end], "has_more": end < len(records)}}, nil
		}
		return map[string]any{"result": map[string]any{"records": records, "has_more": false}}, nil
	}
	if cached, ok := r.cache[key]; ok {
		return cached, nil
	}
	response, err := r.client.Request(ctx, operation, payload)
	if err != nil || apiError(response) != nil {
		return nil, fmt.Errorf("discovery read failed")
	}
	if operation == "get_fields" {
		titles, ids := map[string]bool{}, map[string]bool{}
		for _, raw := range resultSlice(response, "fields") {
			field, _ := raw.(map[string]any)
			title, _ := field["field_title"].(string)
			id, _ := field["field_id"].(string)
			if title == "" || !discoveredIdentifier.MatchString(id) || titles[title] || ids[id] {
				return nil, fmt.Errorf("discovery field identity is invalid or ambiguous")
			}
			titles[title], ids[id] = true, true
		}
	}
	r.cache[key] = response
	return response, nil
}
