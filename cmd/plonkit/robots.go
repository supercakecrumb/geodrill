package main

import "strings"

// robotsRule is one Allow/Disallow line within a matched robots.txt group.
type robotsRule struct {
	path  string
	allow bool
}

// robotsRules is the merged rule set that applies to THIS tool's product
// token, resolved once by parseRobots. See parseRobots' doc comment for what
// "merged" means and why it matters for plonkit.net specifically.
type robotsRules struct {
	rules []robotsRule
}

// rawGroup is one robots.txt group as written in the file: the "User-agent:"
// line(s) it applies to, plus every Allow/Disallow line that follows before
// the next group starts.
type rawGroup struct {
	agents []string
	rules  []robotsRule
}

// parseRobots parses a robots.txt body into the rules that apply to
// ourToken (case-insensitive), per RFC 9309 §2.2:
//   - a group matches if its user-agent line is "*" or a case-insensitive
//     prefix of ourToken;
//   - a literal/specific match (not "*") takes priority over "*" entirely;
//   - if multiple groups tie at the same priority level, their rules are
//     merged (concatenated in file order) — this is not a hypothetical: as
//     fetched 2026-07-18, plonkit.net's robots.txt has TWO separate
//     "User-agent: *" groups (see vibe/plonkit-topics.md for the full text
//     and discussion):
//     1. a Cloudflare-managed block: "User-agent: *" / "Allow: /" (plus a
//     Content-Signal line), followed by named "Disallow: /" groups for
//     several AI crawlers, including literally "ClaudeBot";
//     2. a site-authored block further down: explicit "Allow: /" groups for
//     Googlebot/Bingbot/DuckDuckBot, then a final "User-agent: *" /
//     "Disallow: /".
//     Neither named group's token is a prefix of "geodrill-plonkit-scraper"
//     (this tool doesn't claim to be Googlebot or any of the named bots), so
//     only the two "*" groups apply here, merged in file order.
func parseRobots(body []byte, ourToken string) *robotsRules {
	groups := splitRobotsGroups(body)
	ourToken = strings.ToLower(strings.TrimSpace(ourToken))

	var specific, wildcard []robotsRule
	sawSpecific := false

	for _, g := range groups {
		matched, isWildcard := matchRobotsGroup(g.agents, ourToken)
		if !matched {
			continue
		}
		if isWildcard {
			wildcard = append(wildcard, g.rules...)
			continue
		}
		specific = append(specific, g.rules...)
		sawSpecific = true
	}

	if sawSpecific {
		return &robotsRules{rules: specific}
	}
	return &robotsRules{rules: wildcard}
}

// splitRobotsGroups groups consecutive "User-agent:" lines with the
// Allow/Disallow lines that follow them, up to the next group. Unrecognized
// directives (Sitemap, Crawl-delay, Content-Signal, comments, blank lines)
// are skipped — this tool only needs the Allow/Disallow courtesy check.
func splitRobotsGroups(body []byte) []rawGroup {
	var groups []rawGroup
	var cur *rawGroup
	seenRuleInCur := false

	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := splitRobotsDirective(line)
		if !ok {
			continue
		}
		switch key {
		case "user-agent":
			if cur == nil || seenRuleInCur {
				groups = append(groups, rawGroup{})
				cur = &groups[len(groups)-1]
				seenRuleInCur = false
			}
			cur.agents = append(cur.agents, val)
		case "allow", "disallow":
			if cur == nil {
				continue // malformed (rule before any group) — ignore
			}
			seenRuleInCur = true
			cur.rules = append(cur.rules, robotsRule{path: val, allow: key == "allow"})
		}
	}
	return groups
}

// splitRobotsDirective splits a "Key: value" robots.txt line. Lines with no
// colon are not a directive.
func splitRobotsDirective(line string) (key, val string, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	val = strings.TrimSpace(line[i+1:])
	return key, val, true
}

// matchRobotsGroup reports whether any agent in agents matches ourToken, and
// whether that match came only via the "*" wildcard (lower priority than a
// literal product-token-prefix match per RFC 9309 §2.2.1).
func matchRobotsGroup(agents []string, ourToken string) (matched, isWildcard bool) {
	sawWildcard := false
	for _, a := range agents {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "*" {
			sawWildcard = true
			continue
		}
		if a != "" && strings.HasPrefix(ourToken, a) {
			return true, false
		}
	}
	return sawWildcard, sawWildcard
}

// allowed reports whether path is permitted by r, using the standard
// longest-matching-prefix rule with ties resolved in favor of Allow (RFC
// 9309 §2.2.2: "the least restrictive rule must be used"). A nil/empty rule
// set or a path with no matching rule defaults to allowed.
func (r *robotsRules) allowed(path string) bool {
	if r == nil || len(r.rules) == 0 {
		return true
	}
	bestLen := -1
	bestAllow := true
	for _, rule := range r.rules {
		if rule.path == "" || !strings.HasPrefix(path, rule.path) {
			continue
		}
		l := len(rule.path)
		if l > bestLen || (l == bestLen && rule.allow) {
			bestLen = l
			bestAllow = rule.allow
		}
	}
	if bestLen < 0 {
		return true
	}
	return bestAllow
}
