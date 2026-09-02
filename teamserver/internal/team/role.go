package team

import (
	"fmt"
	"slices"

	legacymcp "github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

type Role string

const (
	RoleReader   Role = "reader"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
	RolePolicy   Role = "policy"
)

func roleRank(role Role) int {
	switch role {
	case RoleReader:
		return 1
	case RoleOperator:
		return 2
	case RoleAdmin, RolePolicy:
		return 3
	default:
		return 0
	}
}

func requiredRank(access legacymcp.ToolAccess) int {
	switch access {
	case legacymcp.ToolAccessReader:
		return roleRank(RoleReader)
	case legacymcp.ToolAccessOperator:
		return roleRank(RoleOperator)
	case legacymcp.ToolAccessAdmin:
		return roleRank(RoleAdmin)
	default:
		return 100
	}
}

func allows(role Role, access legacymcp.ToolAccess) bool {
	return roleRank(role) >= requiredRank(access)
}

func resolveRole(claimed []string, cfg Config) (Role, error) {
	role := Role("")
	for _, value := range claimed {
		switch value {
		case cfg.ReaderRole:
			if roleRank(role) < roleRank(RoleReader) {
				role = RoleReader
			}
		case cfg.OperatorRole:
			if roleRank(role) < roleRank(RoleOperator) {
				role = RoleOperator
			}
		case cfg.AdminRole:
			role = RoleAdmin
		}
	}
	if role == "" {
		return "", fmt.Errorf("token does not contain an authorized team role")
	}
	return role, nil
}

func containsAll(values, required []string) bool {
	for _, value := range required {
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}
