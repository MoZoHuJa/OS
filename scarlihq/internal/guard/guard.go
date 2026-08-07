package guard

import (
	"regexp"
	"strings"
)

// Guard checks commands for dangerous patterns
type Guard struct {
	blockedPatterns []*regexp.Regexp
	hitlPatterns    []*regexp.Regexp
}

// New creates a new Guard with default rules
func New() *Guard {
	blocked := []string{
		`rm\s+-rf\s+/(\s|$)`,
		`rm\s+-rf\s+~`,
		`rm\s+-rf\s+\*`,
		`dd\s+if=.*of=/dev/sd`,
		`mkfs\.\w+\s+/dev/sd`,
		`shutdown`,
		`reboot`,
		`chmod\s+777\s+/`,
		`:\(\)\s*\{\s*:\|:&\s*\};:`,
	}
	hitl := []string{
		`apt\s+install`,
		`apt-get\s+install`,
		`systemctl\s+enable`,
		`docker\s+pull`,
	}

	g := &Guard{}
	for _, p := range blocked {
		g.blockedPatterns = append(g.blockedPatterns, regexp.MustCompile(p))
	}
	for _, p := range hitl {
		g.hitlPatterns = append(g.hitlPatterns, regexp.MustCompile(p))
	}
	return g
}

// Check validates a command and returns action: "blocked", "hitl", or "safe"
func (g *Guard) Check(command string) string {
	cmd := strings.TrimSpace(command)

	for _, p := range g.blockedPatterns {
		if p.MatchString(cmd) {
			return "blocked"
		}
	}

	for _, p := range g.hitlPatterns {
		if p.MatchString(cmd) {
			return "hitl"
		}
	}

	return "safe"
}

// IsBlocked returns true if command is blocked
func (g *Guard) IsBlocked(command string) bool {
	return g.Check(command) == "blocked"
}

// NeedsHITL returns true if command needs human-in-the-loop approval
func (g *Guard) NeedsHITL(command string) bool {
	return g.Check(command) == "hitl"
}
