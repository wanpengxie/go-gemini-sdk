package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	gemini "github.com/wanpengxie/go-gemini-sdk"
	"mvdan.cc/sh/v3/syntax"
)

type decision string

const (
	decisionNone  decision = ""
	decisionAllow decision = "allow"
	decisionDeny  decision = "deny"
	decisionAsk   decision = "ask"
)

type rawPolicyConfig struct {
	Allow       []string          `json:"allow"`
	Deny        []string          `json:"deny"`
	Ask         []string          `json:"ask"`
	Permissions *rawPolicySection `json:"permissions,omitempty"`
}

type rawPolicySection struct {
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
	if cfg.Permissions != nil {
		cfg.Allow = append(cfg.Allow, cfg.Permissions.Allow...)
		cfg.Deny = append(cfg.Deny, cfg.Permissions.Deny...)
		cfg.Ask = append(cfg.Ask, cfg.Permissions.Ask...)
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

	if !strings.Contains(raw, "(") && !strings.Contains(raw, ")") {
		return compiledRule{
			tool: canonicalToolName(raw),
		}, nil
	}

	left := strings.Index(raw, "(")
	right := strings.LastIndex(raw, ")")
	if left <= 0 || right != len(raw)-1 || right <= left {
		return compiledRule{}, fmt.Errorf("invalid rule syntax: %q", raw)
	}

	tool := canonicalToolName(raw[:left])
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

func (p compiledPolicy) decide(call gemini.ToolCallInfo, baseDir string) decision {
	baseDir = normalizeBaseDir(baseDir)

	if isExecuteCall(call) {
		return p.decideExecute(call)
	}
	if isFilesystemCall(call) {
		return p.decideFilesystem(call, baseDir)
	}

	if matchesAnyGeneric(call, p.deny) {
		return decisionDeny
	}
	if matchesAnyGeneric(call, p.ask) {
		return decisionAsk
	}
	if matchesAnyGeneric(call, p.allow) {
		return decisionAllow
	}
	return decisionNone
}

func (p compiledPolicy) decideExecute(call gemini.ToolCallInfo) decision {
	commands, err := extractAtomicShellCommands(call.Args)
	if err != nil || len(commands) == 0 {
		if hasMatchingToolRule(call, p.deny) || hasMatchingToolRule(call, p.ask) || hasMatchingToolRule(call, p.allow) {
			return decisionDeny
		}
		return decisionNone
	}

	if anyCommandMatches(call, commands, p.deny) {
		return decisionDeny
	}
	if anyCommandMatches(call, commands, p.ask) {
		return decisionAsk
	}
	if allCommandsAllowed(call, commands, p.allow) {
		return decisionAllow
	}
	return decisionNone
}

func (p compiledPolicy) decideFilesystem(call gemini.ToolCallInfo, baseDir string) decision {
	paths, err := extractCandidatePaths(call.Args)
	if err != nil || len(paths) == 0 {
		if hasMatchingToolRule(call, p.deny) || hasMatchingToolRule(call, p.ask) || hasMatchingToolRule(call, p.allow) {
			return decisionDeny
		}
		return decisionNone
	}

	targets := normalizeTargetPaths(paths, baseDir)
	if len(targets) == 0 {
		if hasMatchingToolRule(call, p.deny) || hasMatchingToolRule(call, p.ask) || hasMatchingToolRule(call, p.allow) {
			return decisionDeny
		}
		return decisionNone
	}

	if anyPathMatches(call, targets, baseDir, p.deny) {
		return decisionDeny
	}
	if anyPathMatches(call, targets, baseDir, p.ask) {
		return decisionAsk
	}
	if allPathsAllowed(call, targets, baseDir, p.allow) {
		return decisionAllow
	}
	return decisionNone
}

func matchesAnyGeneric(call gemini.ToolCallInfo, rules []compiledRule) bool {
	target := extractCommandLikeText(call.Args)
	for _, rule := range rules {
		if !rule.matchesTool(call) {
			continue
		}
		if !rule.hasPattern {
			return true
		}
		if target == "" {
			continue
		}
		if globMatchFold(rule.pattern, target) {
			return true
		}
	}
	return false
}

func anyCommandMatches(call gemini.ToolCallInfo, commands []string, rules []compiledRule) bool {
	for _, cmd := range commands {
		for _, rule := range rules {
			if !rule.matchesTool(call) {
				continue
			}
			if !rule.hasPattern || globMatchFold(rule.pattern, cmd) {
				return true
			}
		}
	}
	return false
}

func allCommandsAllowed(call gemini.ToolCallInfo, commands []string, allowRules []compiledRule) bool {
	if len(commands) == 0 {
		return false
	}

	hasAllowRule := false
	for _, rule := range allowRules {
		if rule.matchesTool(call) {
			hasAllowRule = true
			break
		}
	}
	if !hasAllowRule {
		return false
	}

	for _, cmd := range commands {
		matched := false
		for _, rule := range allowRules {
			if !rule.matchesTool(call) {
				continue
			}
			if !rule.hasPattern || globMatchFold(rule.pattern, cmd) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func anyPathMatches(call gemini.ToolCallInfo, targets []string, baseDir string, rules []compiledRule) bool {
	for _, target := range targets {
		for _, rule := range rules {
			if !rule.matchesTool(call) {
				continue
			}
			if !rule.hasPattern {
				return true
			}
			if pathGlobMatch(rule.pattern, target, baseDir) {
				return true
			}
		}
	}
	return false
}

func allPathsAllowed(call gemini.ToolCallInfo, targets []string, baseDir string, allowRules []compiledRule) bool {
	if len(targets) == 0 {
		return false
	}

	hasAllowRule := false
	for _, rule := range allowRules {
		if rule.matchesTool(call) {
			hasAllowRule = true
			break
		}
	}
	if !hasAllowRule {
		return false
	}

	for _, target := range targets {
		matched := false
		for _, rule := range allowRules {
			if !rule.matchesTool(call) {
				continue
			}
			if !rule.hasPattern || pathGlobMatch(rule.pattern, target, baseDir) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func hasMatchingToolRule(call gemini.ToolCallInfo, rules []compiledRule) bool {
	for _, rule := range rules {
		if rule.matchesTool(call) {
			return true
		}
	}
	return false
}

func (r compiledRule) matchesTool(call gemini.ToolCallInfo) bool {
	if r.tool == "*" {
		return true
	}
	for _, candidate := range toolCandidates(call) {
		if candidate == r.tool {
			return true
		}
	}
	return false
}

func isExecuteCall(call gemini.ToolCallInfo) bool {
	for _, candidate := range toolCandidates(call) {
		if candidate == "execute" {
			return true
		}
	}
	return false
}

func isFilesystemCall(call gemini.ToolCallInfo) bool {
	for _, candidate := range toolCandidates(call) {
		switch candidate {
		case "read", "edit", "delete":
			return true
		}
	}
	return false
}

func toolCandidates(call gemini.ToolCallInfo) []string {
	seen := make(map[string]struct{}, 2)
	add := func(name string) {
		name = canonicalToolName(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
	}

	add(string(call.ToolKind))
	add(call.ToolName)

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

func canonicalToolName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return ""
	case "*":
		return "*"
	case "bash", "execute", "shell", "command":
		return "execute"
	case "read", "fs_read", "file_read":
		return "read"
	case "edit", "write", "fs_write", "file_write":
		return "edit"
	case "delete", "remove", "unlink", "fs_delete", "file_delete":
		return "delete"
	case "unknown":
		return ""
	default:
		return s
	}
}

func extractAtomicShellCommands(raw json.RawMessage) ([]string, error) {
	script, err := extractShellScript(raw)
	if err != nil {
		return nil, err
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, err
	}

	printer := syntax.NewPrinter()
	commands := make([]string, 0, 4)
	syntax.Walk(file, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		text := normalizeInlineSpaces(renderShellNode(printer, call))
		if text != "" {
			commands = append(commands, text)
		}
		return true
	})

	return commands, nil
}

func renderShellNode(printer *syntax.Printer, node syntax.Node) string {
	var buf bytes.Buffer
	if err := printer.Print(&buf, node); err != nil {
		return ""
	}
	return buf.String()
}

func extractShellScript(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("empty args")
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		asString = strings.TrimSpace(asString)
		if asString == "" {
			return "", errors.New("empty command string")
		}
		return asString, nil
	}

	var asObject map[string]any
	if err := json.Unmarshal(raw, &asObject); err == nil {
		for _, key := range []string{"command", "cmd", "script"} {
			if v, ok := asObject[key].(string); ok {
				v = strings.TrimSpace(v)
				if v != "" {
					return v, nil
				}
			}
		}
		for _, key := range []string{"argv", "args"} {
			if cmd := joinStringArray(asObject[key]); cmd != "" {
				return cmd, nil
			}
		}
		return "", errors.New("command field not found")
	}

	var asArray []any
	if err := json.Unmarshal(raw, &asArray); err == nil {
		if cmd := joinStringArray(asArray); cmd != "" {
			return cmd, nil
		}
		return "", errors.New("empty command array")
	}

	plain := strings.TrimSpace(string(raw))
	if plain == "" {
		return "", errors.New("empty command")
	}
	if strings.HasPrefix(plain, "{") || strings.HasPrefix(plain, "[") {
		return "", errors.New("unsupported command shape")
	}
	return strings.Trim(plain, `"`), nil
}

func joinStringArray(v any) string {
	switch vv := v.(type) {
	case []string:
		parts := make([]string, 0, len(vv))
		for _, item := range vv {
			item = strings.TrimSpace(item)
			if item != "" {
				parts = append(parts, item)
			}
		}
		return strings.Join(parts, " ")
	case []any:
		parts := make([]string, 0, len(vv))
		for _, item := range vv {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func extractCandidatePaths(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("empty args")
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}

	out := make([]string, 0, 4)
	collectPaths("", decoded, &out)
	out = compactStringSlice(out)
	if len(out) == 0 {
		return nil, errors.New("path field not found")
	}
	return out, nil
}

func collectPaths(key string, v any, out *[]string) {
	key = strings.ToLower(strings.TrimSpace(key))

	switch value := v.(type) {
	case string:
		if isPathKey(key) {
			s := strings.TrimSpace(value)
			if s != "" {
				*out = append(*out, s)
			}
		}
	case []any:
		if isPathKey(key) {
			for _, item := range value {
				if s, ok := item.(string); ok {
					s = strings.TrimSpace(s)
					if s != "" {
						*out = append(*out, s)
					}
				}
			}
		}
		for _, item := range value {
			collectPaths("", item, out)
		}
	case map[string]any:
		for childKey, childValue := range value {
			collectPaths(childKey, childValue, out)
		}
	}
}

func isPathKey(key string) bool {
	switch key {
	case "path", "paths", "file", "filepath", "target", "targets", "destination", "dest", "source", "src", "dst":
		return true
	default:
		return false
	}
}

func normalizeTargetPaths(paths []string, baseDir string) []string {
	baseDir = normalizeBaseDir(baseDir)
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		normalized, err := normalizePathTarget(path, baseDir)
		if err != nil {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizePathTarget(target, baseDir string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("empty target path")
	}

	if !filepath.IsAbs(target) {
		target = filepath.Join(baseDir, target)
	}
	target = filepath.Clean(target)
	return filepath.Abs(target)
}

func normalizePathPattern(pattern, baseDir string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", errors.New("empty pattern")
	}

	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(baseDir, pattern)
	}
	pattern = filepath.Clean(pattern)
	return filepath.Abs(pattern)
}

func pathGlobMatch(pattern, target, baseDir string) bool {
	normalizedPattern, err := normalizePathPattern(pattern, baseDir)
	if err != nil {
		return false
	}
	normalizedTarget, err := normalizePathTarget(target, baseDir)
	if err != nil {
		return false
	}

	matched, err := doublestar.Match(filepath.ToSlash(normalizedPattern), filepath.ToSlash(normalizedTarget))
	if err != nil {
		return false
	}
	return matched
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
			if cmd := joinStringArray(m[key]); cmd != "" {
				return cmd
			}
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

func normalizeBaseDir(baseDir string) string {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		wd, err := os.Getwd()
		if err == nil {
			baseDir = wd
		}
	}
	if baseDir == "" {
		baseDir = "."
	}
	abs, err := filepath.Abs(baseDir)
	if err == nil {
		return abs
	}
	return filepath.Clean(baseDir)
}

func compactStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeInlineSpaces(s string) string {
	fields := strings.Fields(strings.TrimSpace(s))
	return strings.Join(fields, " ")
}
