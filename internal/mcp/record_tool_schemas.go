package mcp

func zoopRoleToolSchema() map[string]any {
	return map[string]any{
		"type": "string",
		"enum": []string{"Z-S01", "Z-S02", "Z-S03", "Z-S04", "Z-S05", "Z-S06", "Z-S07", "Z-S08", "Z-S09"},
	}
}

func schemaStatusToolSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"target_role": zoopRoleToolSchema(),
		},
	}
}

func recordApplyToolSchema() map[string]any {
	cellObject := map[string]any{
		"type":                 "object",
		"minProperties":        1,
		"additionalProperties": true,
		"description":          "仅用于兼容性矩阵已验证的复杂单元格对象；关联字段必须改用仅含 record_id 的对象。",
	}
	referenceObject := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"record_id"},
		"properties": map[string]any{
			"record_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		},
	}
	fieldValue := map[string]any{
		"description": "字段标题对应的值。TEXT/PHONE/EMAIL/BARCODE/DATE_TIME 与 SINGLE_SELECT 使用字符串；CHECKBOX 使用布尔；数字类使用数字；REFERENCE 使用 {record_id} 数组；其他复杂字段仅接受已验证的单元格对象数组。",
		// Some MCP clients validate tool arguments with primitive type coercion.
		// A oneOf union can then make false match both boolean and number and be
		// rejected as ambiguous. The runtime still validates the value against
		// the concrete field type from the fixed local Schema mirror, so anyOf
		// preserves the published alternatives without weakening write checks.
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
			map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"anyOf": []any{referenceObject, cellObject}}},
		},
	}
	record := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"values"},
		"properties": map[string]any{
			"record_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "update_records 必填；add_records 禁止携带。"},
			"values": map[string]any{
				"type":                 "object",
				"minProperties":        1,
				"propertyNames":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"additionalProperties": fieldValue,
				"description":          "键必须是当前 target_role 本地 Schema 镜像中的字段标题，不是 field_id。系统自动字段、公式、查找引用和附件不可写。",
			},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"target_role", "operation", "idempotency_key", "source_revision", "records"},
		"properties": map[string]any{
			"target_role":     zoopRoleToolSchema(),
			"operation":       map[string]any{"type": "string", "enum": []string{"add_records", "update_records"}, "description": "add_records 的记录不能有 record_id；update_records 的每条记录必须有真实 record_id。"},
			"idempotency_key": map[string]any{"type": "string", "minLength": 16, "maxLength": 256, "description": "同一次逻辑写入和不确定结果恢复必须复用同一键；它不是业务字段，不能用于记录查询。"},
			"source_revision": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "审计来源标识，不是预览 ID，也不用于记录查询。"},
			"records":         map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": record},
		},
	}
}
