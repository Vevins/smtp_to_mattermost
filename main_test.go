package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/flashmob/go-guerrilla"
	"github.com/stretchr/testify/assert"
	"gopkg.in/gomail.v2"
)

var (
	testSmtpListenHost   = "127.0.0.1"
	testSmtpListenPort   = 22725
	testHTTPServerListen = "127.0.0.1:22780"
)

func makeSmtpConfig() *SmtpConfig {
	return &SmtpConfig{
		smtpListen:      fmt.Sprintf("%s:%d", testSmtpListenHost, testSmtpListenPort),
		smtpPrimaryHost: "testhost",
	}
}

func makeMattermostConfig() *MattermostConfig {
	return &MattermostConfig{
		serverURL:                  "http://" + testHTTPServerListen,
		token:                      "secret-token",
		channelIDs:                 "channel-1,channel-2",
		messageTemplate:            "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}",
		apiTimeoutSeconds:          5,
		forwardedAttachmentMaxSize: 0,
		messageLengthToSendAsFile:  12000,
	}
}

func startSmtp(smtpConfig *SmtpConfig, mattermostConfig *MattermostConfig) guerrilla.Daemon {
	d, err := SmtpStart(smtpConfig, mattermostConfig)
	if err != nil {
		panic(fmt.Sprintf("start error: %s", err))
	}
	waitSmtp(smtpConfig.smtpListen)
	return d
}

func waitSmtp(smtpHost string) {
	for n := 0; n < 100; n++ {
		c, err := smtp.Dial(smtpHost)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func goMailBody(content []byte) gomail.FileSetting {
	return gomail.SetCopyFunc(func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
}

func TestSuccess(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	mattermostConfig := makeMattermostConfig()
	d := startSmtp(smtpConfig, mattermostConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(h)
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NoError(t, err)

	assert.Len(t, h.Posts, len(strings.Split(mattermostConfig.channelIDs, ",")))
	exp := "From: from@test\nTo: to@test\nSubject: \n\nhi"
	assert.Equal(t, exp, h.Posts[0].Message)
}

func TestSuccessCustomFormat(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	mattermostConfig := makeMattermostConfig()
	mattermostConfig.messageTemplate = "Subject: {subject}\\n\\n{body}"
	d := startSmtp(smtpConfig, mattermostConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(h)
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NoError(t, err)

	assert.Len(t, h.Posts, len(strings.Split(mattermostConfig.channelIDs, ",")))
	assert.Equal(t, "Subject: \n\nhi", h.Posts[0].Message)
}

func TestMattermostUnreachable(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	mattermostConfig := makeMattermostConfig()
	d := startSmtp(smtpConfig, mattermostConfig)
	defer d.Shutdown()

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NotNil(t, err)
}

func TestMattermostHTTPError(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	mattermostConfig := makeMattermostConfig()
	d := startSmtp(smtpConfig, mattermostConfig)
	defer d.Shutdown()

	s := HTTPServer(&ErrorHandler{})
	defer s.Shutdown(context.Background())

	err := smtp.SendMail(smtpConfig.smtpListen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	assert.NotNil(t, err)
}

func TestAttachmentsSending(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	mattermostConfig := makeMattermostConfig()
	mattermostConfig.forwardedAttachmentMaxSize = 1024
	d := startSmtp(smtpConfig, mattermostConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", "Text body")
	m.Attach("hey.txt", goMailBody([]byte("hi")))
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	err := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "").DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.Posts, len(strings.Split(mattermostConfig.channelIDs, ",")))
	assert.Len(t, h.Uploads, 2*len(strings.Split(mattermostConfig.channelIDs, ",")))
	assert.Equal(t,
		"From: from@test\nTo: to@test\nSubject: Test subj\n\nText body\n\nAttachments:\n- 📎 hey.txt (text/plain) 2B, sending...\n- 📎 attachment.jpg (image/jpeg) 3B, sending...",
		h.Posts[0].Message,
	)
}

func TestLargeMessageProperlyTruncated(t *testing.T) {
	smtpConfig := makeSmtpConfig()
	mattermostConfig := makeMattermostConfig()
	mattermostConfig.messageLengthToSendAsFile = 100
	mattermostConfig.forwardedAttachmentMaxSize = 1024
	d := startSmtp(smtpConfig, mattermostConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(h)
	defer s.Shutdown(context.Background())

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hello_", 60))

	err := gomail.NewPlainDialer(testSmtpListenHost, testSmtpListenPort, "", "").DialAndSend(m)
	assert.NoError(t, err)

	assert.Len(t, h.Posts, len(strings.Split(mattermostConfig.channelIDs, ",")))
	assert.Len(t, h.Uploads, len(strings.Split(mattermostConfig.channelIDs, ",")))
	assert.Equal(t,
		"From: from@test\nTo: to@test\nSubject: Test subj\n\nHello_Hello_Hello_Hello_Hello_Hello_He\n\n[truncated]",
		h.Posts[0].Message,
	)
	assert.Equal(t, "full_message.txt", h.Uploads[0].Filename)
}

func HTTPServer(handler http.Handler) *http.Server {
	h := &http.Server{Addr: testHTTPServerListen, Handler: handler}
	ln, err := net.Listen("tcp", h.Addr)
	if err != nil {
		panic(err)
	}
	go func() {
		h.Serve(ln)
	}()
	return h
}

type RecordedPost struct {
	ChannelID string
	Message   string
	FileIDs   []string
}

type RecordedUpload struct {
	ChannelID string
	Filename  string
	Content   []byte
}

type SuccessHandler struct {
	Posts   []RecordedPost
	Uploads []RecordedUpload
}

func NewSuccessHandler() *SuccessHandler {
	return &SuccessHandler{
		Posts:   []RecordedPost{},
		Uploads: []RecordedUpload{},
	}
}

func (s *SuccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v4/posts":
		var req MattermostCreatePostRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			panic(err)
		}
		s.Posts = append(s.Posts, RecordedPost{
			ChannelID: req.ChannelID,
			Message:   req.Message,
			FileIDs:   req.FileIDs,
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"post-1"}`))
	case r.URL.Path == "/api/v4/files":
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			panic(err)
		}
		file, header, err := r.FormFile("files")
		if err != nil {
			panic(err)
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			panic(err)
		}
		s.Uploads = append(s.Uploads, RecordedUpload{
			ChannelID: r.FormValue("channel_id"),
			Filename:  header.Filename,
			Content:   content,
		})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"file_infos":[{"id":"file-%d"}]}`, len(s.Uploads))
	default:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Error"))
	}
}

type ErrorHandler struct{}

func (s *ErrorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte("Error"))
}
