package channels

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/voice"
)

const (
	transcriptionTimeout = 30 * time.Second
	sendTimeout          = 10 * time.Second
	typingInterval       = 8 * time.Second
	typingMaxDuration    = 5 * time.Minute
)

type DiscordChannel struct {
	*BaseChannel
	session     *discordgo.Session
	config      config.DiscordConfig
	transcriber *voice.GroqTranscriber
	ctx         context.Context
	typingMu    sync.Mutex
	typingTasks map[string]typingTask
	typingSeq   uint64
}

type typingTask struct {
	id     uint64
	cancel context.CancelFunc
}

func NewDiscordChannel(cfg config.DiscordConfig, bus *bus.MessageBus) (*DiscordChannel, error) {
	session, err := discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create discord session: %w", err)
	}

	base := NewBaseChannel("discord", cfg, bus, cfg.AllowFrom)

	return &DiscordChannel{
		BaseChannel: base,
		session:     session,
		config:      cfg,
		transcriber: nil,
		ctx:         context.Background(),
		typingTasks: make(map[string]typingTask),
	}, nil
}

func (c *DiscordChannel) SetTranscriber(transcriber *voice.GroqTranscriber) {
	c.transcriber = transcriber
}

func (c *DiscordChannel) getContext() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func (c *DiscordChannel) Start(ctx context.Context) error {
	logger.InfoC("discord", "Starting Discord bot")

	c.ctx = ctx
	c.session.AddHandler(c.handleMessage)

	if err := c.session.Open(); err != nil {
		return fmt.Errorf("failed to open discord session: %w", err)
	}

	c.setRunning(true)

	botUser, err := c.session.User("@me")
	if err != nil {
		return fmt.Errorf("failed to get bot user: %w", err)
	}
	logger.InfoCF("discord", "Discord bot connected", map[string]any{
		"username": botUser.Username,
		"user_id":  botUser.ID,
	})

	return nil
}

func (c *DiscordChannel) Stop(ctx context.Context) error {
	logger.InfoC("discord", "Stopping Discord bot")
	c.setRunning(false)
	c.stopAllTyping()

	if err := c.session.Close(); err != nil {
		return fmt.Errorf("failed to close discord session: %w", err)
	}

	return nil
}

func (c *DiscordChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("discord bot not running")
	}

	channelID := msg.ChatID
	if channelID == "" {
		return fmt.Errorf("channel ID is empty")
	}
	defer func() {
		// Stop typing only when the final response of this request is sent.
		// This avoids non-session/system messages stopping another active request.
		if msg.IsFinal {
			c.stopTyping(msg.RequestID)
		}
	}()

	if msg.Control {
		return nil
	}

	message := msg.Content

	// 使用传入的 ctx 进行超时控制
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.session.ChannelMessageSend(channelID, message)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("failed to send discord message: %w", err)
		}
		return nil
	case <-sendCtx.Done():
		return fmt.Errorf("send message timeout: %w", sendCtx.Err())
	}
}

// appendContent 安全地追加内容到现有文本
func appendContent(content, suffix string) string {
	if content == "" {
		return suffix
	}
	return content + "\n" + suffix
}

func (c *DiscordChannel) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil || m.Author == nil {
		return
	}

	if m.Author.ID == s.State.User.ID {
		return
	}

	// 检查白名单，避免为被拒绝的用户下载附件和转录
	if !c.IsAllowed(m.Author.ID) {
		logger.DebugCF("discord", "Message rejected by allowlist", map[string]any{
			"user_id": m.Author.ID,
		})
		return
	}

	senderID := m.Author.ID
	senderName := m.Author.Username
	if m.Author.Discriminator != "" && m.Author.Discriminator != "0" {
		senderName += "#" + m.Author.Discriminator
	}

	content := m.Content
	mediaPaths := make([]string, 0, len(m.Attachments))
	localFiles := make([]string, 0, len(m.Attachments))

	// 确保临时文件在函数返回时被清理
	defer func() {
		for _, file := range localFiles {
			if err := os.Remove(file); err != nil {
				logger.DebugCF("discord", "Failed to cleanup temp file", map[string]any{
					"file":  file,
					"error": err.Error(),
				})
			}
		}
	}()

	for _, attachment := range m.Attachments {
		isAudio := utils.IsAudioFile(attachment.Filename, attachment.ContentType)

		if isAudio {
			localPath := c.downloadAttachment(attachment.URL, attachment.Filename)
			if localPath != "" {
				localFiles = append(localFiles, localPath)

				transcribedText := ""
				if c.transcriber != nil && c.transcriber.IsAvailable() {
					ctx, cancel := context.WithTimeout(c.getContext(), transcriptionTimeout)
					result, err := c.transcriber.Transcribe(ctx, localPath)
					cancel() // 立即释放context资源，避免在for循环中泄漏

					if err != nil {
						logger.ErrorCF("discord", "Voice transcription failed", map[string]any{
							"error": err.Error(),
						})
						transcribedText = fmt.Sprintf("[audio: %s (transcription failed)]", attachment.Filename)
					} else {
						transcribedText = fmt.Sprintf("[audio transcription: %s]", result.Text)
						logger.DebugCF("discord", "Audio transcribed successfully", map[string]any{
							"text": result.Text,
						})
					}
				} else {
					transcribedText = fmt.Sprintf("[audio: %s]", attachment.Filename)
				}

				content = appendContent(content, transcribedText)
			} else {
				logger.WarnCF("discord", "Failed to download audio attachment", map[string]any{
					"url":      attachment.URL,
					"filename": attachment.Filename,
				})
				mediaPaths = append(mediaPaths, attachment.URL)
				content = appendContent(content, fmt.Sprintf("[attachment: %s]", attachment.URL))
			}
		} else {
			mediaPaths = append(mediaPaths, attachment.URL)
			content = appendContent(content, fmt.Sprintf("[attachment: %s]", attachment.URL))
		}
	}

	if content == "" && len(mediaPaths) == 0 {
		return
	}

	if content == "" {
		content = "[media only]"
	}

	logger.DebugCF("discord", "Received message", map[string]any{
		"sender_name": senderName,
		"sender_id":   senderID,
		"preview":     utils.Truncate(content, 50),
	})

	metadata := map[string]string{
		"message_id":   m.ID,
		"request_id":   m.ID,
		"user_id":      senderID,
		"username":     m.Author.Username,
		"display_name": senderName,
		"guild_id":     m.GuildID,
		"channel_id":   m.ChannelID,
		"is_dm":        fmt.Sprintf("%t", m.GuildID == ""),
	}

	c.startTyping(m.ID, m.ChannelID)
	c.HandleMessage(senderID, m.ChannelID, content, mediaPaths, metadata)
}

func (c *DiscordChannel) typingKey(requestID string) string {
	if requestID == "" {
		return ""
	}
	return fmt.Sprintf("%s:req:%s", c.Name(), requestID)
}

func (c *DiscordChannel) startTyping(requestID, channelID string) {
	key := c.typingKey(requestID)
	if key == "" {
		return
	}

	c.typingMu.Lock()
	if _, exists := c.typingTasks[key]; exists {
		c.typingMu.Unlock()
		return
	}

	typingCtx, cancel := context.WithCancel(context.Background())
	c.typingSeq++
	taskID := c.typingSeq
	c.typingTasks[key] = typingTask{id: taskID, cancel: cancel}
	c.typingMu.Unlock()

	go func() {
		defer c.cleanupTypingTask(key, taskID)

		sendTyping := func() {
			if err := c.session.ChannelTyping(channelID); err != nil {
				logger.DebugCF("discord", "Failed to send typing indicator", map[string]any{
					"channel_id": channelID,
					"error":      err.Error(),
				})
			}
		}

		sendTyping()

		ticker := time.NewTicker(typingInterval)
		defer ticker.Stop()

		timeout := time.NewTimer(typingMaxDuration)
		defer timeout.Stop()

		for {
			select {
			case <-typingCtx.Done():
				return
			case <-timeout.C:
				logger.DebugCF("discord", "Typing indicator auto-stopped on timeout", map[string]any{
					"session_key": key,
				})
				return
			case <-ticker.C:
				sendTyping()
			}
		}
	}()
}

func (c *DiscordChannel) stopTyping(requestID string) {
	key := c.typingKey(requestID)
	if key == "" {
		return
	}

	c.typingMu.Lock()
	task, exists := c.typingTasks[key]
	if exists {
		delete(c.typingTasks, key)
	}
	c.typingMu.Unlock()

	if exists {
		task.cancel()
	}
}

func (c *DiscordChannel) cleanupTypingTask(key string, taskID uint64) {
	c.typingMu.Lock()
	current, exists := c.typingTasks[key]
	if exists && current.id == taskID {
		delete(c.typingTasks, key)
	}
	c.typingMu.Unlock()
}

func (c *DiscordChannel) stopAllTyping() {
	c.typingMu.Lock()
	cancellers := make([]context.CancelFunc, 0, len(c.typingTasks))
	for key, task := range c.typingTasks {
		cancellers = append(cancellers, task.cancel)
		delete(c.typingTasks, key)
	}
	c.typingMu.Unlock()

	for _, cancel := range cancellers {
		cancel()
	}
}

func (c *DiscordChannel) downloadAttachment(url, filename string) string {
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "discord",
	})
}
