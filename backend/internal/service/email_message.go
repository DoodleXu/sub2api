package service

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

type smtpMessage struct {
	envelopeFrom string
	envelopeTo   string
	data         []byte
}

func buildSMTPMessage(config *SMTPConfig, to, subject, body string) (smtpMessage, error) {
	return buildSMTPMessageWithHeaders(config, to, subject, body, nil)
}

func buildSMTPMessageWithHeaders(config *SMTPConfig, to, subject, body string, headers map[string]string) (smtpMessage, error) {
	if config == nil {
		return smtpMessage{}, errors.New("missing SMTP configuration")
	}

	fromAddress, err := parseSMTPAddress(config.From, "from")
	if err != nil {
		return smtpMessage{}, err
	}
	recipientAddress, err := parseSMTPAddress(to, "recipient")
	if err != nil {
		return smtpMessage{}, err
	}
	messageID := notificationEmailHeaderValue(headers, "Message-ID")
	if messageID == "" {
		messageID, err = generateEmailMessageID(fromAddress.Address, config.Host)
		if err != nil {
			return smtpMessage{}, fmt.Errorf("generate message ID: %w", err)
		}
	} else if !validSMTPMessageID(messageID) {
		return smtpMessage{}, errors.New("invalid Message-ID header")
	}

	fromName := sanitizeEmailHeader(config.FromName)
	if strings.TrimSpace(fromName) == "" {
		fromName = fromAddress.Name
	}
	fromHeader := (&mail.Address{
		Name:    fromName,
		Address: fromAddress.Address,
	}).String()
	toHeader := (&mail.Address{
		Name:    recipientAddress.Name,
		Address: recipientAddress.Address,
	}).String()
	subjectHeader := mime.QEncoding.Encode("UTF-8", sanitizeEmailHeader(subject))

	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&message, "To: %s\r\n", toHeader)
	fmt.Fprintf(&message, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&message, "Message-ID: %s\r\n", messageID)
	fmt.Fprintf(&message, "Subject: %s\r\n", subjectHeader)
	fmt.Fprint(&message, formatEmailHeaders(headers))
	alternative := multipart.NewWriter(&message)
	fmt.Fprintf(&message, "MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", alternative.Boundary())
	if err := writeSMTPAlternativePart(alternative, "text/plain; charset=UTF-8", notificationEmailHTMLToText(body)); err != nil {
		return smtpMessage{}, err
	}
	if err := writeSMTPAlternativePart(alternative, "text/html; charset=UTF-8", body); err != nil {
		return smtpMessage{}, err
	}
	if err := alternative.Close(); err != nil {
		return smtpMessage{}, fmt.Errorf("close multipart email body: %w", err)
	}

	return smtpMessage{
		envelopeFrom: fromAddress.Address,
		envelopeTo:   recipientAddress.Address,
		data:         message.Bytes(),
	}, nil
}

func writeSMTPAlternativePart(writer *multipart.Writer, contentType, body string) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Transfer-Encoding", "quoted-printable")
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create email body part: %w", err)
	}
	encoded := quotedprintable.NewWriter(part)
	if _, err := encoded.Write([]byte(body)); err != nil {
		return fmt.Errorf("encode email body part: %w", err)
	}
	if err := encoded.Close(); err != nil {
		return fmt.Errorf("close email body part: %w", err)
	}
	return nil
}

func notificationEmailHTMLToText(body string) string {
	doc, err := xhtml.Parse(strings.NewReader(body))
	if err != nil {
		return strings.TrimSpace(body)
	}
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			text := strings.Join(strings.Fields(node.Data), " ")
			if text != "" {
				if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "\n") {
					_ = builder.WriteByte(' ')
				}
				_, _ = builder.WriteString(text)
			}
		}
		if node.Type == xhtml.ElementNode && node.Data == "br" {
			_ = builder.WriteByte('\n')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == xhtml.ElementNode {
			switch node.Data {
			case "p", "div", "li", "tr", "section", "h1", "h2", "h3", "h4":
				_ = builder.WriteByte('\n')
			}
		}
	}
	walk(doc)
	lines := strings.Split(builder.String(), "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n")
}

func parseSMTPAddress(value, field string) (*mail.Address, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("invalid SMTP %s address: contains a line break", field)
	}

	cleaned := strings.TrimSpace(value)
	address, err := mail.ParseAddress(cleaned)
	if err != nil || strings.TrimSpace(address.Address) == "" {
		if err == nil {
			err = fmt.Errorf("address is empty")
		}
		return nil, fmt.Errorf("invalid SMTP %s address: %w", field, err)
	}
	return address, nil
}

func notificationEmailHeaderValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validSMTPMessageID(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 5 && strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") &&
		strings.Contains(value, "@") && !strings.ContainsAny(value, "\r\n\t ")
}

func generateEmailMessageID(fromAddress, smtpHost string) (string, error) {
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return "", err
	}

	domain := strings.TrimSpace(sanitizeEmailHeader(smtpHost))
	if at := strings.LastIndexByte(fromAddress, '@'); at >= 0 && at < len(fromAddress)-1 {
		domain = fromAddress[at+1:]
	}
	domain = strings.Trim(domain, "[]<>")
	if domain == "" {
		domain = "localhost"
	}

	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(randomID), domain), nil
}
