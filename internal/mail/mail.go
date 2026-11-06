// Package mail builds MIME newsletters with inline images and sends them
// over SMTP.
package mail

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Attachment struct {
	CID         string
	ContentType string
	Data        []byte
}

type Message struct {
	FromName  string
	FromEmail string
	ReplyTo   string
	To        string
	Subject   string
	Text      string
	HTML      string
	Inline    []Attachment
	Date      time.Time
}

// Build renders the message as multipart/related(alternative(text, html), images).
func Build(m Message) ([]byte, error) {
	for _, value := range []string{m.FromEmail, m.To, m.ReplyTo, m.Subject, m.FromName} {
		if strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("header values must be single-line")
		}
	}
	from := (&mail.Address{Name: m.FromName, Address: m.FromEmail}).String()
	to := (&mail.Address{Address: m.To}).String()
	date := m.Date
	if date.IsZero() {
		date = time.Now()
	}
	domain := "silo-newsletter.local"
	if at := strings.LastIndex(m.FromEmail, "@"); at >= 0 {
		domain = m.FromEmail[at+1:]
	}

	var body bytes.Buffer
	related := multipart.NewWriter(&body)
	var alternativeBody bytes.Buffer
	alternative := multipart.NewWriter(&alternativeBody)
	if err := writeQuotedPrintable(alternative, "text/plain; charset=utf-8", m.Text); err != nil {
		return nil, err
	}
	if err := writeQuotedPrintable(alternative, "text/html; charset=utf-8", m.HTML); err != nil {
		return nil, err
	}
	if err := alternative.Close(); err != nil {
		return nil, err
	}
	part, err := related.CreatePart(textproto.MIMEHeader{"Content-Type": {"multipart/alternative; boundary=" + alternative.Boundary()}})
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(alternativeBody.Bytes()); err != nil {
		return nil, err
	}
	for _, attachment := range m.Inline {
		if strings.ContainsAny(attachment.CID, "<>\r\n \"") {
			return nil, errors.New("invalid inline image id")
		}
		header := textproto.MIMEHeader{
			"Content-Type":              {attachment.ContentType},
			"Content-Transfer-Encoding": {"base64"},
			"Content-ID":                {"<" + attachment.CID + ">"},
			"Content-Disposition":       {"inline"},
		}
		part, err := related.CreatePart(header)
		if err != nil {
			return nil, err
		}
		if err := writeBase64(part, attachment.Data); err != nil {
			return nil, err
		}
	}
	if err := related.Close(); err != nil {
		return nil, err
	}

	var message bytes.Buffer
	headers := [][2]string{
		{"From", from},
		{"To", to},
		{"Subject", mime.QEncoding.Encode("utf-8", m.Subject)},
		{"Date", date.Format(time.RFC1123Z)},
		{"Message-ID", "<" + randomID() + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/related; type="multipart/alternative"; boundary=` + related.Boundary()},
	}
	if m.ReplyTo != "" {
		headers = append(headers, [2]string{"Reply-To", (&mail.Address{Address: m.ReplyTo}).String()})
	}
	for _, header := range headers {
		fmt.Fprintf(&message, "%s: %s\r\n", header[0], header[1])
	}
	message.WriteString("\r\n")
	message.Write(body.Bytes())
	return message.Bytes(), nil
}

func writeQuotedPrintable(writer *multipart.Writer, contentType, content string) error {
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {contentType}, "Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return err
	}
	encoder := quotedprintable.NewWriter(part)
	if _, err := encoder.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return encoder.Close()
}

func writeBase64(part interface{ Write([]byte) (int, error) }, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		if _, err := part.Write([]byte(encoded[:76] + "\r\n")); err != nil {
			return err
		}
		encoded = encoded[76:]
	}
	_, err := part.Write([]byte(encoded + "\r\n"))
	return err
}

func randomID() string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

type Server struct {
	Host     string
	Port     int
	Username string
	Password string
	// Security is "starttls", "tls", or "none".
	Security string
	Timeout  time.Duration
}

// Send delivers one message. The password is never included in errors.
func (s Server) Send(fromEmail, to string, raw []byte) error {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	var connection net.Conn
	var err error
	dialer := &net.Dialer{Timeout: timeout}
	if s.Security == "tls" {
		connection, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		connection, err = dialer.Dial("tcp", address)
	}
	if err != nil {
		return fmt.Errorf("could not connect to SMTP server %s: %w", address, err)
	}
	_ = connection.SetDeadline(time.Now().Add(timeout * 4))
	client, err := smtp.NewClient(connection, s.Host)
	if err != nil {
		connection.Close()
		return fmt.Errorf("SMTP greeting failed: %w", err)
	}
	defer client.Close()
	if s.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not offer STARTTLS; refusing to send credentials in plain text")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return errors.New("SMTP login failed; check SmtpUsername and SmtpPassword (for Gmail use an app password)")
		}
	}
	if err := client.Mail(fromEmail); err != nil {
		return fmt.Errorf("SMTP rejected the sender: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP rejected the recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}
	if _, err := writer.Write(raw); err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SMTP did not accept the message: %w", err)
	}
	return client.Quit()
}
