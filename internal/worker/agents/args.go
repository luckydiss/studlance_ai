package agents

import "github.com/luckydiss/studlance_ai/internal/config"

// CodexArgs builds the argv for codex exec (ARCHITECTURE.md, "Запуск CLI").
// threadID == "" starts a fresh session in dir; otherwise the thread is
// resumed. The prompt is always read from stdin ("-").
func CodexArgs(cmdCfg config.WorkerCommand, dir, threadID string) []string {
	args := []string{"exec"}
	if threadID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox")
	if threadID == "" {
		args = append(args, "-C", dir)
		args = append(args, cmdCfg.Args...)
	} else {
		args = append(args, cmdCfg.Args...)
		args = append(args, threadID)
	}
	return append(args, "-")
}

// ClaudeArgs builds the argv for claude (ARCHITECTURE.md, "Запуск CLI"):
// a fresh session with --session-id, or --resume of an existing one. The
// prompt is read from stdin (implied by -p).
func ClaudeArgs(cmdCfg config.WorkerCommand, sessionID string, resume bool) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	args = append(args, cmdCfg.Args...)
	if resume {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	return args
}
