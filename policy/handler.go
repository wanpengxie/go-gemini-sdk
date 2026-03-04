package policy

import (
	"context"
	"strings"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

// NewHandlerFromJSON parses policy JSON and returns a CanUseToolFunc callback.
//
// Supported JSON format:
//
//	{
//	  "allow": ["Read", "Bash(ls *)"],
//	  "deny":  ["Bash(rm -rf *)"],
//	  "ask":   ["Bash(git push *)"]
//	}
func NewHandlerFromJSON(raw string) (gemini.CanUseToolFunc, error) {
	p, err := parsePolicyConfig(raw)
	if err != nil {
		return nil, err
	}

	return func(_ context.Context, call gemini.ToolCallInfo, options []gemini.PermissionOption) (string, error) {
		switch p.decide(call) {
		case decisionDeny:
			return findOptionByPrefix(options, "reject_"), nil
		case decisionAsk:
			return findOptionByPrefix(options, "ask_"), nil
		case decisionAllow:
			return findOptionByPrefix(options, "allow_"), nil
		default:
			return "", nil
		}
	}, nil
}

func findOptionByPrefix(options []gemini.PermissionOption, prefix string) string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return ""
	}
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(id), prefix) {
			return id
		}
	}
	return ""
}
