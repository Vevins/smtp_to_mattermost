package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	units "github.com/docker/go-units"
	"github.com/flashmob/go-guerrilla"
	"github.com/flashmob/go-guerrilla/backends"
	"github.com/flashmob/go-guerrilla/log"
	"github.com/flashmob/go-guerrilla/mail"
	"github.com/jhillyerd/enmime/v2"
	"github.com/urfave/cli/v2"
)

var (
	Version string = "UNKNOWN_RELEASE"
	logger  log.Logger
)

const BodyTruncated = "\n\n[truncated]"

type SmtpConfig struct {
	smtpListen          string
	smtpPrimaryHost     string
	smtpMaxEnvelopeSize int64
	logLevel            string
}

type MattermostConfig struct {
	serverURL                  string
	token                      string
	channelIDs                 string
	messageTemplate            string
	apiTimeoutSeconds          float64
	forwardedAttachmentMaxSize int
	messageLengthToSendAsFile  uint
}

type MattermostFileInfo struct {
	ID string `json:"id"`
}

type MattermostUploadResponse struct {
	FileInfos []MattermostFileInfo `json:"file_infos"`
}

type MattermostCreatePostRequest struct {
	ChannelID string   `json:"channel_id"`
	Message   string   `json:"message"`
	FileIDs   []string `json:"file_ids,omitempty"`
}

type FormattedEmail struct {
	text        string
	attachments []*FormattedAttachment
}

type FormattedAttachment struct {
	filename string
	caption  string
	content  []byte
}

func GetHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		panic(fmt.Sprintf("Unable to detect hostname: %s", err))
	}
	return hostname
}

func main() {
	app := cli.NewApp()
	app.Name = "smtp_to_mattermost"
	app.Usage = "A simple program that listens for SMTP and forwards all incoming Email messages to Mattermost."
	app.Version = Version
	app.Action = func(c *cli.Context) error {
		smtpMaxEnvelopeSize, err := units.FromHumanSize(c.String("smtp-max-envelope-size"))
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}
		forwardedAttachmentMaxSize, err := units.FromHumanSize(c.String("forwarded-attachment-max-size"))
		if err != nil {
			fmt.Printf("%s\n", err)
			os.Exit(1)
		}

		smtpConfig := &SmtpConfig{
			smtpListen:          c.String("smtp-listen"),
			smtpPrimaryHost:     c.String("smtp-primary-host"),
			smtpMaxEnvelopeSize: smtpMaxEnvelopeSize,
			logLevel:            c.String("log-level"),
		}
		mattermostConfig := &MattermostConfig{
			serverURL:                  strings.TrimRight(c.String("mattermost-server-url"), "/"),
			token:                      c.String("mattermost-token"),
			channelIDs:                 c.String("mattermost-channel-ids"),
			messageTemplate:            c.String("message-template"),
			apiTimeoutSeconds:          c.Float64("mattermost-api-timeout-seconds"),
			forwardedAttachmentMaxSize: int(forwardedAttachmentMaxSize),
			messageLengthToSendAsFile:  c.Uint("message-length-to-send-as-file"),
		}
		d, err := SmtpStart(smtpConfig, mattermostConfig)
		if err != nil {
			panic(fmt.Sprintf("start error: %s", err))
		}
		sigHandler(d)
		return nil
	}
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "smtp-listen",
			Value:   "127.0.0.1:2525",
			Usage:   "SMTP: TCP address to listen to",
			EnvVars: []string{"ST_SMTP_LISTEN"},
		},
		&cli.StringFlag{
			Name:    "smtp-primary-host",
			Value:   GetHostname(),
			Usage:   "SMTP: primary host",
			EnvVars: []string{"ST_SMTP_PRIMARY_HOST"},
		},
		&cli.StringFlag{
			Name:    "smtp-max-envelope-size",
			Usage:   "Max size of an incoming Email. Examples: 5k, 10m.",
			Value:   "50m",
			EnvVars: []string{"ST_SMTP_MAX_ENVELOPE_SIZE"},
		},
		&cli.StringFlag{
			Name:     "mattermost-server-url",
			Usage:    "Mattermost: base server URL, e.g. https://mattermost.example.com",
			EnvVars:  []string{"ST_MATTERMOST_SERVER_URL"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "mattermost-token",
			Usage:    "Mattermost: bot or personal access token",
			EnvVars:  []string{"ST_MATTERMOST_TOKEN"},
			Required: true,
		},
		&cli.StringFlag{
			Name:     "mattermost-channel-ids",
			Usage:    "Mattermost: comma-separated list of target channel IDs",
			EnvVars:  []string{"ST_MATTERMOST_CHANNEL_IDS"},
			Required: true,
		},
		&cli.StringFlag{
			Name:    "message-template",
			Usage:   "Mattermost message template",
			Value:   "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}",
			EnvVars: []string{"ST_MATTERMOST_MESSAGE_TEMPLATE"},
		},
		&cli.Float64Flag{
			Name:    "mattermost-api-timeout-seconds",
			Usage:   "HTTP timeout used for requests to the Mattermost API",
			Value:   30,
			EnvVars: []string{"ST_MATTERMOST_API_TIMEOUT_SECONDS"},
		},
		&cli.StringFlag{
			Name: "forwarded-attachment-max-size",
			Usage: "Max size of an attachment to be forwarded to Mattermost. " +
				"0 disables forwarding. Examples: 5k, 10m.",
			Value:   "10m",
			EnvVars: []string{"ST_FORWARDED_ATTACHMENT_MAX_SIZE"},
		},
		&cli.UintFlag{
			Name: "message-length-to-send-as-file",
			Usage: "If message length is greater than this number, it is sent " +
				"truncated followed by a text file containing the full message.",
			Value:   12000,
			EnvVars: []string{"ST_MESSAGE_LENGTH_TO_SEND_AS_FILE"},
		},
		&cli.StringFlag{
			Name:    "log-level",
			Usage:   "Logging level (info, debug, error, panic).",
			Value:   "info",
			EnvVars: []string{"ST_LOG_LEVEL"},
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}
}

func SmtpStart(smtpConfig *SmtpConfig, mattermostConfig *MattermostConfig) (guerrilla.Daemon, error) {
	cfg := &guerrilla.AppConfig{LogFile: log.OutputStdout.String(), LogLevel: smtpConfig.logLevel}
	cfg.AllowedHosts = []string{"."}

	sc := guerrilla.ServerConfig{
		IsEnabled:       true,
		ListenInterface: smtpConfig.smtpListen,
		MaxSize:         smtpConfig.smtpMaxEnvelopeSize,
	}
	cfg.Servers = append(cfg.Servers, sc)

	bcfg := backends.BackendConfig{
		"save_workers_size":  3,
		"save_process":       "HeadersParser|Header|Hasher|MattermostBot",
		"log_received_mails": true,
		"primary_mail_host":  smtpConfig.smtpPrimaryHost,
		"gw_save_timeout":    "600s",
	}
	cfg.BackendConfig = bcfg

	daemon := guerrilla.Daemon{Config: cfg}
	daemon.AddProcessor("MattermostBot", MattermostBotProcessorFactory(mattermostConfig))

	logger = daemon.Log()
	err := daemon.Start()
	return daemon, err
}

func MattermostBotProcessorFactory(mattermostConfig *MattermostConfig) func() backends.Decorator {
	return func() backends.Decorator {
		return func(p backends.Processor) backends.Processor {
			return backends.ProcessWith(
				func(e *mail.Envelope, task backends.SelectTask) (backends.Result, error) {
					if task == backends.TaskSaveMail {
						err := SendEmailToMattermost(e, mattermostConfig)
						if err != nil {
							return backends.NewResult(fmt.Sprintf("421 Error: %s", err)), err
						}
					}
					return p.Process(e, task)
				},
			)
		}
	}
}

func SendEmailToMattermost(e *mail.Envelope, mattermostConfig *MattermostConfig) error {
	message, err := FormatEmail(e, mattermostConfig)
	if err != nil {
		return err
	}

	client := http.Client{
		Timeout: time.Duration(mattermostConfig.apiTimeoutSeconds * float64(time.Second)),
	}

	for _, channelID := range strings.Split(mattermostConfig.channelIDs, ",") {
		channelID = strings.TrimSpace(channelID)
		if channelID == "" {
			continue
		}
		err := SendMessageToChannel(message, channelID, mattermostConfig, &client)
		if err != nil {
			return errors.New(SanitizeToken(err.Error(), mattermostConfig.token))
		}
	}

	return nil
}

func SendMessageToChannel(message *FormattedEmail, channelID string, mattermostConfig *MattermostConfig, client *http.Client) error {
	fileIDs := make([]string, 0, len(message.attachments))
	for _, attachment := range message.attachments {
		fileID, err := UploadFileToChannel(attachment, channelID, mattermostConfig, client)
		if err != nil {
			return err
		}
		fileIDs = append(fileIDs, fileID)
	}

	body, err := json.Marshal(&MattermostCreatePostRequest{
		ChannelID: channelID,
		Message:   message.text,
		FileIDs:   fileIDs,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, mattermostConfig.serverURL+"/api/v4/posts", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+mattermostConfig.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("non-2xx response from Mattermost create post: (%d) %s", resp.StatusCode, EscapeMultiLine(mustReadAll(resp.Body)))
	}
	return nil
}

func UploadFileToChannel(attachment *FormattedAttachment, channelID string, mattermostConfig *MattermostConfig, client *http.Client) (string, error) {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)

	if err := w.WriteField("channel_id", channelID); err != nil {
		return "", err
	}
	fw, err := w.CreateFormFile("files", attachment.filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(attachment.content); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, mattermostConfig.serverURL+"/api/v4/files", buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+mattermostConfig.token)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("non-2xx response from Mattermost file upload: (%d) %s", resp.StatusCode, EscapeMultiLine(mustReadAll(resp.Body)))
	}

	upload := &MattermostUploadResponse{}
	if err := json.NewDecoder(resp.Body).Decode(upload); err != nil {
		return "", fmt.Errorf("error parsing json body of file upload: %v", err)
	}
	if len(upload.FileInfos) == 0 || upload.FileInfos[0].ID == "" {
		return "", fmt.Errorf("mattermost file upload did not return file_infos")
	}
	return upload.FileInfos[0].ID, nil
}

func FormatEmail(e *mail.Envelope, mattermostConfig *MattermostConfig) (*FormattedEmail, error) {
	reader := e.NewReader()
	env, err := enmime.ReadEnvelope(reader)
	if err != nil {
		return nil, fmt.Errorf("%s\n\nerror occurred during email parsing: %v", e, err)
	}

	text := env.Text
	attachmentsDetails := []string{}
	attachments := []*FormattedAttachment{}

	doParts := func(emoji string, parts []*enmime.Part) {
		for _, part := range parts {
			if bytes.Equal(part.Content, []byte(env.Text)) {
				continue
			}
			if text == "" && part.ContentType == "text/plain" && part.FileName == "" {
				text = string(part.Content)
				continue
			}

			action := "discarded"
			contentType := GuessContentType(part.ContentType, part.FileName)
			if len(part.Content) <= mattermostConfig.forwardedAttachmentMaxSize {
				action = "sending..."
				filename := part.FileName
				if filename == "" {
					filename = fallbackAttachmentName(contentType)
				}
				attachments = append(attachments, &FormattedAttachment{
					filename: filename,
					caption:  part.FileName,
					content:  part.Content,
				})
			}

			attachmentsDetails = append(attachmentsDetails, fmt.Sprintf(
				"- %s %s (%s) %s, %s",
				emoji,
				displayAttachmentName(part.FileName),
				contentType,
				units.HumanSize(float64(len(part.Content))),
				action,
			))
		}
	}

	doParts("🔗", env.Inlines)
	doParts("📎", env.Attachments)
	for _, part := range env.OtherParts {
		attachmentsDetails = append(attachmentsDetails, fmt.Sprintf(
			"- ❔ %s (%s) %s, discarded",
			displayAttachmentName(part.FileName),
			GuessContentType(part.ContentType, part.FileName),
			units.HumanSize(float64(len(part.Content))),
		))
	}
	for _, envErr := range env.Errors {
		logger.Errorf("Envelope error: %s", envErr.Error())
	}

	if text == "" {
		text = e.Data.String()
	}

	formattedAttachmentsDetails := ""
	if len(attachmentsDetails) > 0 {
		formattedAttachmentsDetails = "Attachments:\n" + strings.Join(attachmentsDetails, "\n")
	}

	fullMessageText, truncatedMessageText := FormatMessage(
		e.MailFrom.String(),
		JoinEmailAddresses(e.RcptTo),
		env.GetHeader("subject"),
		text,
		formattedAttachmentsDetails,
		mattermostConfig,
	)

	if truncatedMessageText == "" {
		return &FormattedEmail{text: fullMessageText, attachments: attachments}, nil
	}

	if len(fullMessageText) > mattermostConfig.forwardedAttachmentMaxSize {
		return nil, fmt.Errorf(
			"the message length (%d) is larger than `forwarded-attachment-max-size` (%d)",
			len(fullMessageText),
			mattermostConfig.forwardedAttachmentMaxSize,
		)
	}

	attachments = append([]*FormattedAttachment{{
		filename: "full_message.txt",
		caption:  "Full message",
		content:  []byte(fullMessageText),
	}}, attachments...)

	return &FormattedEmail{text: truncatedMessageText, attachments: attachments}, nil
}

func FormatMessage(from string, to string, subject string, text string, formattedAttachmentsDetails string, mattermostConfig *MattermostConfig) (string, string) {
	fullMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace(text),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(mattermostConfig.messageTemplate),
	)

	fullMessageRunes := []rune(fullMessageText)
	if uint(len(fullMessageRunes)) <= mattermostConfig.messageLengthToSendAsFile {
		return fullMessageText, ""
	}

	emptyMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace("."+BodyTruncated),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(mattermostConfig.messageTemplate),
	)
	emptyMessageRunes := []rune(emptyMessageText)
	if uint(len(emptyMessageRunes)) >= mattermostConfig.messageLengthToSendAsFile {
		return fullMessageText, string(fullMessageRunes[:mattermostConfig.messageLengthToSendAsFile])
	}

	maxBodyLength := mattermostConfig.messageLengthToSendAsFile - uint(len(emptyMessageRunes))
	truncatedMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace(fmt.Sprintf("%s%s",
				string([]rune(strings.TrimSpace(text))[:maxBodyLength]), BodyTruncated)),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(mattermostConfig.messageTemplate),
	)
	if uint(len([]rune(truncatedMessageText))) > mattermostConfig.messageLengthToSendAsFile {
		panic(fmt.Errorf("unexpected length of truncated message:\n%d\n%s", maxBodyLength, truncatedMessageText))
	}

	return fullMessageText, truncatedMessageText
}

func GuessContentType(contentType string, filename string) string {
	if contentType != "application/octet-stream" {
		return contentType
	}
	guessedType := mime.TypeByExtension(filepath.Ext(filename))
	if guessedType != "" {
		return guessedType
	}
	return contentType
}

func JoinEmailAddresses(a []mail.Address) string {
	s := make([]string, 0, len(a))
	for _, aa := range a {
		s = append(s, aa.String())
	}
	return strings.Join(s, ", ")
}

func EscapeMultiLine(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func SanitizeToken(s string, token string) string {
	return strings.ReplaceAll(s, token, "***")
}

func fallbackAttachmentName(contentType string) string {
	if contentType == "" {
		return "attachment.bin"
	}
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return "attachment" + exts[0]
	}
	return "attachment.bin"
}

func displayAttachmentName(name string) string {
	if name == "" {
		return "(unnamed)"
	}
	return name
}

func mustReadAll(r io.Reader) []byte {
	body, _ := io.ReadAll(r)
	return body
}

func sigHandler(d guerrilla.Daemon) {
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGKILL, os.Kill)
	for range signalChannel {
		logger.Info("Shutdown signal caught")
		go func() {
			select {
			case <-time.After(60 * time.Second):
				logger.Error("graceful shutdown timed out")
				os.Exit(1)
			}
		}()
		d.Shutdown()
		logger.Info("Shutdown completed, exiting.")
		return
	}
}
