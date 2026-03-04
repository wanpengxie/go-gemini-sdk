package policy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

type decision string

const (
	decisionNone  decision = ""
	decisionAllow decision = "allow"
	decisionDeny  decision = "deny"
	decisionAsk   decision = "ask"
)

type rawPolicyConfig struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
	Ask   []string `json:"ask"`
}

type compiledPolicy struct {
	allow []compiledRule
	deny  []compiledRule
	ask   []compiledRule
}

type compiledRule struct {
	tool       string
	pattern    string
	hasPattern bool
}

func parsePolicyConfig(raw string) (compiledPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return compiledPolicy{}, fmt.Errorf("empty policy config")
	}

	var cfg rawPolicyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return compiledPolicy{}, fmt.Errorf("parse policy json: %w", err)
	}

	allow, err := compileRuleList(cfg.Allow)
	if err != nil {
		return compiledPolicy{}, fmt.Errorf("compile allow rules: %w", err)
	}
	deny, err := compileRuleList(cfg.Deny)
	if err != nil {
		return compiledPolicy{}, fmt.Errorf("compile deny rules: %w", err)
	}
	ask, err := compileRuleList(cfg.Ask)
	if err != nil {
		return compiledPolicy{}, fmt.Errorf("compile ask rules: %w", err)
	}

	return compiledPolicy{
		allow: allow,
		deny:  deny,
		ask:   ask,
	}, nil
}

func compileRuleList(items []string) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(items))
	for _, item := range items {
		rule, err := parseRule(item)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

func parseRule(raw string) (compiledRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return compiledRule{}, fmt.Errorf("empty rule")
	}

	// Supports simple tool match, e.g. "Read" / "Bash",
	// and command-aware syntax, e.g. "Bash(rm -rf *)".
	if !strings.Contains(raw, "(") && !strings.Contains(raw, ")") {
		return compiledRule{
			tool: strings.ToLower(raw),
		}, nil
	}

	left := strings.Index(raw, "(")
	right := strings.LastIndex(raw, ")")
	if left <= 0 || right != len(raw)-1 || right <= left {
		return compiledRule{}, fmt.Errorf("invalid rule syntax: %q", raw)
	}

	tool := strings.ToLower(strings.TrimSpace(raw[:left]))
	pattern := strings.TrimSpace(raw[left+1 : right])
	if tool == "" {
		return compiledRule{}, fmt.Errorf("invalid rule syntax: %q", raw)
	}
	if pattern == "" {
		pattern = "*"
	}

	return compiledRule{
		tool:       tool,
		pattern:    pattern,
		hasPattern: true,
	}, nil
}

func (p compiledPolicy) decide(call gemini.ToolCallInfo) decision {
	if matchesAny(call, p.deny) {
		return decisionDeny
	}
	if matchesAny(call, p.ask) {
		return decisionAsk
	}
	if matchesAny(call, p.allow) {
		return decisionAllow
	}
	return decisionNone
}

func matchesAny(call gemini.ToolCallInfo, rules []compiledRule) bool {
	for _, rule := range rules {
		if rule.matches(call) {
			return true
		}
	}
	return false
}

func (r compiledRule) matches(call gemini.ToolCallInfo) bool {
	if !toolMatches(r.tool, call) {
		return false
	}
	if !r.hasPattern {
		return true
	}
	target := extractCommandLikeText(call.Args)
	if target == "" {
		return false
	}
	return globMatchFold(r.pattern, target)
}

func toolMatches(ruleTool string, call gemini.ToolCallInfo) bool {
	ruleTool = strings.ToLower(strings.TrimSpace(ruleTool))
	if ruleTool == "" {
		return false
	}
	if ruleTool == "*" {
		return true
	}

	toolName := strings.ToLower(strings.TrimSpace(call.ToolName))
	toolKind := strings.ToLower(strings.TrimSpace(string(call.ToolKind)))

	return toolName == ruleTool || toolKind == ruleTool
}

func extractCommandLikeText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		for _, key := range []string{"command", "cmd", "script"} {
			if v, ok := m[key].(string); ok {
				v = strings.TrimSpace(v)
				if v != "" {
					return v
				}
			}
		}
		for _, key := range []string{"argv", "args"} {
			if v, ok := m[key].([]any); ok {
				parts := make([]string, 0, len(v))
				for _, item := range v {
					if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
						parts = append(parts, strings.TrimSpace(s))
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, " ")
				}
			}
		}
	}

	var list []any
	if err := json.Unmarshal(raw, &list); err == nil {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}

	return strings.TrimSpace(string(raw))
}

func globMatchFold(pattern, value string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	value = strings.ToLower(strings.TrimSpace(value))
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}

	regex := regexp.QuoteMeta(pattern)
	regex = strings.ReplaceAll(regex, "\\*", ".*")
	regex = strings.ReplaceAll(regex, "\\?", ".")
	compiled, err := regexp.Compile("^" + regex + "$")
	if err != nil {
		return pattern == value
	}
	return compiled.MatchString(value)
}
