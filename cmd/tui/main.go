// Command tui is a local terminal interface for the GopherAI HTTP API.
// It retains its in-process token only in memory after startup and is intended
// to run beside a trusted local or private deployment of the API.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	requestTimeout     = 30 * time.Second
	chatRequestTimeout = 2 * time.Minute
)

type terminalApp struct {
	client        *client
	input         *bufio.Reader
	passwordInput *os.File
	output        io.Writer
	clear         bool
}

func main() {
	server := flag.String("server", envOrDefault("GOPHERAI_TUI_SERVER", "http://127.0.0.1:9090"), "GopherAI server URL")
	token := flag.String("token", os.Getenv("GOPHERAI_TUI_TOKEN"), "bearer token (avoid command-line tokens on multi-user hosts)")
	noClear := flag.Bool("no-clear", false, "do not clear the terminal between screens")
	flag.Parse()

	api, err := newClient(*server, *token, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "TUI configuration error:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := terminalApp{
		client:        api,
		input:         bufio.NewReader(os.Stdin),
		passwordInput: os.Stdin,
		output:        os.Stdout,
		clear:         !*noClear,
	}
	if err := app.run(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "TUI stopped:", err)
		os.Exit(1)
	}
}

func (app *terminalApp) run(ctx context.Context) error {
	for {
		app.renderMenu()
		choice, err := app.prompt("选择操作")
		if err != nil {
			return err
		}
		switch strings.ToLower(choice) {
		case "1":
			app.showHealth(ctx)
		case "2":
			app.login(ctx)
		case "3":
			app.showSessions(ctx)
		case "4":
			app.showHistory(ctx)
		case "5":
			app.chat(ctx, true)
		case "6":
			app.chat(ctx, false)
		case "7":
			app.client.token = ""
			fmt.Fprintln(app.output, "已清除内存中的登录令牌。")
		case "q", "quit", "exit":
			fmt.Fprintln(app.output, "再见。")
			return nil
		default:
			fmt.Fprintln(app.output, "无效选项。")
		}
		if err := app.pause(); err != nil {
			return err
		}
	}
}

func (app *terminalApp) renderMenu() {
	if app.clear {
		fmt.Fprint(app.output, "\033[2J\033[H")
	}
	fmt.Fprintln(app.output, "GopherAI TUI")
	fmt.Fprintln(app.output, strings.Repeat("=", 44))
	fmt.Fprintln(app.output, "服务:", app.client.serverURL)
	if app.client.token == "" {
		fmt.Fprintln(app.output, "登录: 未登录")
	} else {
		fmt.Fprintln(app.output, "登录: 已登录（令牌仅保存在内存）")
	}
	fmt.Fprintln(app.output, "")
	fmt.Fprintln(app.output, "1. 健康检查")
	fmt.Fprintln(app.output, "2. 登录")
	fmt.Fprintln(app.output, "3. 查看会话")
	fmt.Fprintln(app.output, "4. 查看会话历史")
	fmt.Fprintln(app.output, "5. 新建会话并聊天")
	fmt.Fprintln(app.output, "6. 在已有会话中聊天")
	fmt.Fprintln(app.output, "7. 登出（清除内存令牌）")
	fmt.Fprintln(app.output, "q. 退出")
	fmt.Fprintln(app.output, "")
}

func (app *terminalApp) showHealth(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	live, liveErr := app.client.live(ctx)
	ready, readyErr := app.client.ready(ctx)
	if liveErr != nil {
		fmt.Fprintln(app.output, "存活检查失败:", liveErr)
	} else {
		fmt.Fprintf(app.output, "存活检查: %s (%s)\n", live.Status, live.Phase)
	}
	if readyErr != nil {
		fmt.Fprintln(app.output, "就绪检查失败:", readyErr)
		return
	}
	fmt.Fprintln(app.output, "就绪检查:", ready.Status)
	names := make([]string, 0, len(ready.Checks))
	for name := range ready.Checks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		check := ready.Checks[name]
		detail := fmt.Sprintf("%s", check.Status)
		if check.LatencyMS > 0 {
			detail += fmt.Sprintf(" · %dms", check.LatencyMS)
		}
		if check.ErrorCode != "" {
			detail += " · " + check.ErrorCode
		}
		fmt.Fprintf(app.output, "  - %s: %s\n", name, detail)
	}
}

func (app *terminalApp) login(parent context.Context) {
	username, err := app.prompt("用户名")
	if err != nil {
		fmt.Fprintln(app.output, "读取用户名失败:", err)
		return
	}
	password, err := app.promptPassword("密码")
	if err != nil {
		fmt.Fprintln(app.output, "读取密码失败:", err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	if err := app.client.login(ctx, username, password); err != nil {
		fmt.Fprintln(app.output, "登录失败:", err)
		return
	}
	fmt.Fprintln(app.output, "登录成功。")
}

func (app *terminalApp) showSessions(parent context.Context) []sessionInfo {
	cursor := ""
	for {
		page, err := app.loadSessionsPage(parent, cursor)
		if err != nil {
			fmt.Fprintln(app.output, "加载会话失败:", err)
			return nil
		}
		if len(page.Sessions) == 0 {
			fmt.Fprintln(app.output, "暂无会话。")
			return nil
		}
		app.renderSessions(page.Sessions)
		if !pageHasMore(page.HasMore, page.NextCursor) {
			return page.Sessions
		}

		choice, err := app.prompt("输入 n 加载更多，直接回车返回")
		if err != nil || strings.TrimSpace(choice) == "" {
			return page.Sessions
		}
		if isLoadMoreChoice(choice) {
			cursor = page.NextCursor
			continue
		}
		fmt.Fprintln(app.output, "输入无效。")
	}
}

func (app *terminalApp) showHistory(parent context.Context) {
	if app.client.token == "" {
		fmt.Fprintln(app.output, "请先登录。")
		return
	}
	sessionID, ok := app.chooseSession(parent)
	if !ok {
		return
	}
	cursor := ""
	for {
		page, err := app.loadHistoryPage(parent, sessionID, cursor)
		if err != nil {
			fmt.Fprintln(app.output, "加载历史失败:", err)
			return
		}
		if len(page.History) == 0 {
			fmt.Fprintln(app.output, "该会话还没有消息。")
			return
		}
		app.renderHistory(page.History)
		if !pageHasMore(page.HasMore, page.NextCursor) {
			return
		}

		choice, err := app.prompt("输入 n 加载更早消息，直接回车返回")
		if err != nil || strings.TrimSpace(choice) == "" {
			return
		}
		if isLoadMoreChoice(choice) {
			cursor = page.NextCursor
			continue
		}
		fmt.Fprintln(app.output, "输入无效。")
	}
}

func (app *terminalApp) chat(parent context.Context, newSession bool) {
	if app.client.token == "" {
		fmt.Fprintln(app.output, "请先登录。")
		return
	}
	modelID, ok := app.chooseModel(parent)
	if !ok {
		return
	}
	var sessionID string
	if !newSession {
		var selected bool
		sessionID, selected = app.chooseSession(parent)
		if !selected {
			return
		}
	}
	question, err := app.prompt("问题")
	if err != nil || strings.TrimSpace(question) == "" {
		fmt.Fprintln(app.output, "问题不能为空。")
		return
	}
	// Model generation has a longer server-side deadline than ordinary API
	// requests. Keep the TUI context long enough for the default model timeout
	// plus network overhead, without making health/list operations wait as long.
	ctx, cancel := context.WithTimeout(parent, chatRequestTimeout)
	defer cancel()
	var response chatResponse
	if newSession {
		response, err = app.client.createChat(ctx, question, modelID)
	} else {
		response, err = app.client.sendChat(ctx, sessionID, question, modelID)
	}
	if err != nil {
		fmt.Fprintln(app.output, "聊天失败:", err)
		return
	}
	if response.SessionID != "" {
		fmt.Fprintln(app.output, "会话:", response.SessionID)
	}
	fmt.Fprintln(app.output, "\nAI:")
	fmt.Fprintln(app.output, response.Content)
}

func (app *terminalApp) chooseModel(parent context.Context) (string, bool) {
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	models, err := app.client.models(ctx)
	if err != nil {
		fmt.Fprintln(app.output, "加载模型失败:", err)
		return "", false
	}
	chatModels := make([]catalogModel, 0, len(models))
	for _, model := range models {
		if model.Available && model.Capabilities.Chat {
			chatModels = append(chatModels, model)
		}
	}
	if len(chatModels) == 0 {
		fmt.Fprintln(app.output, "没有可用的聊天模型。")
		return "", false
	}
	for index, model := range chatModels {
		fmt.Fprintf(app.output, "%d. %s (%s)\n", index+1, model.DisplayName, model.ID)
	}
	choice, err := app.prompt("模型编号（默认 1）")
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(choice) == "" {
		return chatModels[0].ID, true
	}
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(chatModels) {
		fmt.Fprintln(app.output, "模型编号无效。")
		return "", false
	}
	return chatModels[index-1].ID, true
}

func (app *terminalApp) loadSessionsPage(parent context.Context, cursor string) (sessionsPage, error) {
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	return app.client.sessionsPage(ctx, cursor)
}

func (app *terminalApp) loadHistoryPage(parent context.Context, sessionID, cursor string) (historyPage, error) {
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	return app.client.historyPage(ctx, sessionID, cursor)
}

func (app *terminalApp) renderSessions(sessions []sessionInfo) {
	for index, session := range sessions {
		title := strings.TrimSpace(session.Title)
		if title == "" {
			title = "未命名会话"
		}
		fmt.Fprintf(app.output, "%d. %s\n   %s\n", index+1, title, session.ID)
	}
}

func (app *terminalApp) renderHistory(history []historyItem) {
	for _, message := range history {
		role := "AI"
		if message.IsUser {
			role = "你"
		}
		fmt.Fprintf(app.output, "[%s] %s\n\n", role, message.Content)
	}
}

func pageHasMore(hasMore bool, nextCursor string) bool {
	return hasMore && strings.TrimSpace(nextCursor) != ""
}

func isLoadMoreChoice(choice string) bool {
	choice = strings.ToLower(strings.TrimSpace(choice))
	return choice == "n" || choice == "more"
}

func (app *terminalApp) chooseSession(parent context.Context) (string, bool) {
	cursor := ""
	for {
		page, err := app.loadSessionsPage(parent, cursor)
		if err != nil {
			fmt.Fprintln(app.output, "加载会话失败:", err)
			return "", false
		}
		if len(page.Sessions) == 0 {
			fmt.Fprintln(app.output, "暂无会话。")
			return "", false
		}
		app.renderSessions(page.Sessions)

		label := "会话编号"
		if pageHasMore(page.HasMore, page.NextCursor) {
			label += "（输入 n 加载更多）"
		}
		choice, err := app.prompt(label)
		if err != nil {
			return "", false
		}
		if isLoadMoreChoice(choice) && pageHasMore(page.HasMore, page.NextCursor) {
			cursor = page.NextCursor
			continue
		}
		index, err := strconv.Atoi(choice)
		if err != nil || index < 1 || index > len(page.Sessions) {
			fmt.Fprintln(app.output, "会话编号无效。")
			return "", false
		}
		return page.Sessions[index-1].ID, true
	}
}

func (app *terminalApp) prompt(label string) (string, error) {
	fmt.Fprintf(app.output, "%s: ", label)
	value, err := app.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if errors.Is(err, io.EOF) && value == "" {
		return "", io.EOF
	}
	return value, nil
}

// promptPassword disables terminal echo for interactive use. The reader
// fallback keeps the TUI testable and permits piped automation, where callers
// are responsible for protecting stdin.
func (app *terminalApp) promptPassword(label string) (string, error) {
	fmt.Fprintf(app.output, "%s: ", label)
	if app.passwordInput != nil && term.IsTerminal(int(app.passwordInput.Fd())) {
		value, err := term.ReadPassword(int(app.passwordInput.Fd()))
		fmt.Fprintln(app.output)
		return strings.TrimSpace(string(value)), err
	}
	value, err := app.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if errors.Is(err, io.EOF) && value == "" {
		return "", io.EOF
	}
	return value, nil
}

func (app *terminalApp) pause() error {
	_, err := app.prompt("回车继续")
	return err
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
