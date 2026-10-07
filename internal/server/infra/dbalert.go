package infra

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/skssmd/graft/internal/server/ssh"
)

// Bot tokens look like 123456789:ABC-def_GHI; chat ids are integers, negative
// for groups and channels.
var (
	telegramToken  = regexp.MustCompile(`^[0-9]{5,}:[A-Za-z0-9_-]{20,}$`)
	telegramChatID = regexp.MustCompile(`^-?[0-9]{3,}$`)
)

// parseChatID returns the chat id of the most recent message in a getUpdates
// response, or "" when nobody has messaged the bot yet.
func parseChatID(body []byte) (string, error) {
	var resp struct {
		OK     bool `json:"ok"`
		Result []struct {
			Message struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"message"`
			ChannelPost struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
			} `json:"channel_post"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("telegram rejected the token")
	}
	for i := len(resp.Result) - 1; i >= 0; i-- {
		if id := resp.Result[i].Message.Chat.ID; id != 0 {
			return fmt.Sprint(id), nil
		}
		if id := resp.Result[i].ChannelPost.Chat.ID; id != 0 {
			return fmt.Sprint(id), nil
		}
	}
	return "", nil
}

// DetectChatID asks Telegram which chat last wrote to the bot, so the user does
// not have to dig the id out of getUpdates by hand.
func DetectChatID(token string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("https://api.telegram.org/bot" + token + "/getUpdates")
	if err != nil {
		return "", fmt.Errorf("could not reach Telegram")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return parseChatID(body)
}

// SetupAlerts stores a Telegram bot for this database's backup alerts and sends
// a test message. Leaving the token blank turns alerts off.
func SetupAlerts(client *ssh.Client, db string, in *bufio.Reader, stdout, stderr io.Writer) error {
	if err := requireConfig(client, db); err != nil {
		return err
	}
	if err := installScript(client, stderr); err != nil {
		return err
	}
	tgPath := backupDir + "/" + db + ".telegram"
	prev := parseEnv(mustCat(client, tgPath))

	p := &prompter{in: in, out: stdout}
	fmt.Fprintf(stdout, "\n📨 Telegram alerts for database '%s'\n", db)
	fmt.Fprintln(stdout, "   You get a message when a scheduled backup fails, when no backup has succeeded")
	fmt.Fprintln(stdout, "   for more than two intervals, and when a restore or 'prune <id>' runs.")
	fmt.Fprintln(stdout, "\n   1. In Telegram, message @BotFather, send /newbot and copy the token it gives you.")
	fmt.Fprintln(stdout, "   2. Open a chat with your new bot and send it any message (for a group, add the bot first).")
	fmt.Fprintln(stdout, "----------------------------------------------------------------")

	token := p.secret("Bot token (blank to turn alerts off)", prev["TELEGRAM_BOT_TOKEN"] != "")
	if token == "" {
		token = prev["TELEGRAM_BOT_TOKEN"]
	}
	if token == "" {
		if client.RunCommand("test -f "+shQuote(tgPath), nil, nil) == nil && p.confirm("Turn Telegram alerts off for this database?", true) {
			_ = client.RunCommand("rm -f "+shQuote(tgPath), nil, nil)
			fmt.Fprintln(stdout, "Alerts are off.")
		}
		return nil
	}
	if !telegramToken.MatchString(token) {
		return fmt.Errorf("that does not look like a bot token (expected 123456789:AbC...)")
	}

	chat := prev["TELEGRAM_CHAT_ID"]
	if detected, err := DetectChatID(token); err != nil {
		fmt.Fprintf(stdout, "⚠️  Could not look up your chat automatically: %v\n", err)
	} else if detected != "" {
		chat = p.ask("Chat id (found from your last message to the bot)", detected)
	}
	if chat == "" {
		chat = p.ask("Chat id (send the bot a message first, or enter it manually)", "")
	}
	if !telegramChatID.MatchString(chat) {
		return fmt.Errorf("a chat id is a number such as 123456789 or -1001234567890; send your bot a message and try again")
	}

	content := fmt.Sprintf("TELEGRAM_BOT_TOKEN=%s\nTELEGRAM_CHAT_ID=%s\n", token, chat)
	if err := writeRemote(client, tgPath, content, 0600); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "\n📨 Sending a test message...")
	if err := client.RunCommand(scriptCmd(db, "alert-test"), stdout, stderr); err != nil {
		return fmt.Errorf("the test message was not delivered (the server needs curl and access to api.telegram.org); alerts are saved but may not work")
	}
	fmt.Fprintln(stdout, "✅ Alerts are on. Check your Telegram for the test message.")
	return nil
}

func mustCat(client *ssh.Client, path string) string {
	out, _ := client.GetCommandOutput("cat " + shQuote(path) + " 2>/dev/null")
	return strings.TrimSpace(out)
}
