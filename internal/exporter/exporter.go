// Package exporter generates MHTML files from chat message slices.
// MHTML (MIME HTML) is a single-file web archive format supported by most browsers.
package exporter

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
	"time"

	"qqviewer/internal/avatar"
	"qqviewer/internal/db"
	"qqviewer/internal/parser"
)

// ExportOptions controls how messages are exported.
type ExportOptions struct {
	TableName   string
	TableKind   string   // "group" or "buddy"
	TableID     string   // group number or buddy QQ
	Keyword     string
	SenderUins  []uint64
	TimeFrom    int64 // Unix timestamp, 0 = no limit
	TimeTo      int64 // Unix timestamp, 0 = no limit
	ChunkSize   int  // messages per MHTML file (bulk export)
	MyQQ        uint64
	BubbleRight bool
	ImageBase   string // images.base_dir
}

// BulkExportToZip fetches all matching messages from the DB and writes
// multiple MHTML files (chunked) into a ZIP archive written to w.
func BulkExportToZip(
	w io.Writer,
	database *db.DB,
	cache *avatar.Cache,
	opts ExportOptions,
) error {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 500
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	offset := 0
	chunkIdx := 1
	for {
		msgs, total, err := fetchChunk(database, opts, offset, opts.ChunkSize)
		if err != nil {
			return fmt.Errorf("fetch chunk %d: %w", chunkIdx, err)
		}
		if len(msgs) == 0 {
			break
		}

		// Build title for this chunk
		title := buildTitle(opts, chunkIdx, offset+1, offset+len(msgs), int(total))

		var buf bytes.Buffer
		if err := WriteMHTML(&buf, msgs, cache, opts, title); err != nil {
			return fmt.Errorf("write mhtml chunk %d: %w", chunkIdx, err)
		}

		fname := fmt.Sprintf("chat_%s_part%03d.mhtml", sanitizeFilename(opts.TableID), chunkIdx)
		fw, err := zw.Create(fname)
		if err != nil {
			return fmt.Errorf("zip create %s: %w", fname, err)
		}
		if _, err := fw.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("zip write %s: %w", fname, err)
		}

		offset += len(msgs)
		chunkIdx++
		if offset >= int(total) {
			break
		}
	}

	return nil
}

// WriteMHTML renders messages into a self-contained MHTML document written to w.
func WriteMHTML(
	w io.Writer,
	msgs []db.Message,
	cache *avatar.Cache,
	opts ExportOptions,
	title string,
) error {
	boundary := "----=_NextPart_QQViewer_" + fmt.Sprintf("%d", time.Now().UnixNano())

	// Collect unique avatars to embed
	avatarData := collectAvatars(msgs, cache)

	// Build HTML body
	htmlBody := buildHTMLBody(msgs, cache, opts, avatarData, title)

	// Write MHTML envelope
	mw := multipart.NewWriter(w)
	mw.SetBoundary(boundary)

	// MHTML header
	fmt.Fprintf(w, "From: QQ Chat Viewer\r\n")
	fmt.Fprintf(w, "Subject: %s\r\n", title)
	fmt.Fprintf(w, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(w, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(w, "Content-Type: multipart/related;\r\n\tboundary=\"%s\";\r\n\ttype=\"text/html\"\r\n", boundary)
	fmt.Fprintf(w, "\r\n")

	// Part 1: HTML
	{
		h := make(textproto.MIMEHeader)
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Transfer-Encoding", "quoted-printable")
		h.Set("Content-Location", "chat.html")
		pw, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		if _, err := pw.Write([]byte(htmlBody)); err != nil {
			return err
		}
	}

	// Parts 2+: embedded avatars as base64
	for uin, data := range avatarData {
		if len(data) == 0 {
			continue
		}
		ct := sniffImageType(data)
		cid := fmt.Sprintf("avatar_%d@qqviewer", uin)

		h := make(textproto.MIMEHeader)
		h.Set("Content-Type", ct)
		h.Set("Content-Transfer-Encoding", "base64")
		h.Set("Content-ID", "<"+cid+">")
		h.Set("Content-Location", fmt.Sprintf("avatar_%d", uin))
		pw, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		enc := base64.StdEncoding.EncodeToString(data)
		// Write in 76-char lines per MIME spec
		for len(enc) > 76 {
			pw.Write([]byte(enc[:76] + "\r\n"))
			enc = enc[76:]
		}
		if len(enc) > 0 {
			pw.Write([]byte(enc + "\r\n"))
		}
	}

	return mw.Close()
}

// ---- Internal helpers ----

// fetchChunk fetches a slice of messages using QueryExport (supports time range at SQL level).
func fetchChunk(database *db.DB, opts ExportOptions, offset, limit int) ([]db.Message, int64, error) {
	q := db.ExportQuery{
		Table:      opts.TableName,
		Keyword:    opts.Keyword,
		SenderUins: opts.SenderUins,
		TimeFrom:   opts.TimeFrom,
		TimeTo:     opts.TimeTo,
	}
	return database.QueryExport(q, offset, limit)
}

// collectAvatars fetches avatar image bytes for all unique senders.
func collectAvatars(msgs []db.Message, cache *avatar.Cache) map[uint64][]byte {
	seen := make(map[uint64]bool)
	result := make(map[uint64][]byte)
	for _, m := range msgs {
		if seen[m.SenderUin] {
			continue
		}
		seen[m.SenderUin] = true
		data := cache.AvatarBytes(m.SenderUin)
		result[m.SenderUin] = data // may be nil if not cached
	}
	return result
}

// buildHTMLBody generates the full HTML document for a chunk of messages.
func buildHTMLBody(
	msgs []db.Message,
	cache *avatar.Cache,
	opts ExportOptions,
	avatarData map[uint64][]byte,
	title string,
) string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>`)
	sb.WriteString(html.EscapeString(title))
	sb.WriteString(`</title>
<style>
`)
	sb.WriteString(inlineCSS())
	sb.WriteString(`
</style>
</head>
<body>
<div class="chat-header">
  <h2>`)
	sb.WriteString(html.EscapeString(title))
	sb.WriteString(`</h2>
  <p class="meta">导出时间：`)
	sb.WriteString(html.EscapeString(time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString(` &nbsp;|&nbsp; 共 `)
	sb.WriteString(fmt.Sprintf("%d", len(msgs)))
	sb.WriteString(` 条消息</p>
</div>
<div class="messages">
`)

	lastTimestamp := int64(0)
	for _, m := range msgs {
		// Time divider every 5 minutes
		if lastTimestamp == 0 || (m.Time-lastTimestamp) > 300 {
			sb.WriteString(`  <div class="time-divider"><span>`)
			sb.WriteString(html.EscapeString(formatTime(m.Time)))
			sb.WriteString("</span></div>\n")
		}
		lastTimestamp = m.Time

		isSelf := opts.MyQQ > 0 && m.SenderUin == opts.MyQQ
		side := "left"
		if isSelf && opts.BubbleRight {
			side = "right"
		}

		nick := cache.Nickname(m.SenderUin)
		if nick == "" {
			nick = fmt.Sprintf("%d", m.SenderUin)
		}

		// Avatar: use embedded CID if available, else SVG data URI
		var avatarSrc string
		if data, ok := avatarData[m.SenderUin]; ok && len(data) > 0 {
			ct := sniffImageType(data)
			avatarSrc = fmt.Sprintf("cid:avatar_%d@qqviewer", m.SenderUin)
			_ = ct
		} else {
			avatarSrc = defaultAvatarDataURI(nick)
		}

		// Parse message HTML with images embedded as base64 data URIs for self-contained MHTML
		msgHTML := parser.ParseDecodedMsgForExport(m.DecodedMsg, opts.ImageBase)

		sb.WriteString(fmt.Sprintf(`  <div class="msg-row %s">
    <img class="msg-avatar" src="%s" alt="%s">
    <div class="msg-content">
      <div class="msg-nick">%s</div>
      <div class="msg-bubble">%s</div>
      <div class="msg-time">%s</div>
    </div>
  </div>
`,
			html.EscapeString(side),
			html.EscapeString(avatarSrc),
			html.EscapeString(nick),
			html.EscapeString(nick),
			msgHTML,
			html.EscapeString(formatTime(m.Time)),
		))
	}

	sb.WriteString("</div>\n</body>\n</html>\n")
	return sb.String()
}

// sniffImageType returns a MIME type for image bytes.
func sniffImageType(data []byte) string {
	if len(data) < 4 {
		return "image/jpeg"
	}
	switch {
	case bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}):
		return "image/png"
	case bytes.HasPrefix(data, []byte{'G', 'I', 'F'}):
		return "image/gif"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8}):
		return "image/jpeg"
	case bytes.HasPrefix(data, []byte{'<', 's', 'v', 'g'}) ||
		bytes.HasPrefix(data, []byte{'<', 'S', 'V', 'G'}) ||
		bytes.Contains(data[:min(len(data), 64)], []byte("svg")):
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// defaultAvatarDataURI returns a simple SVG data URI as fallback avatar.
func defaultAvatarDataURI(label string) string {
	initials := label
	if len([]rune(initials)) > 2 {
		r := []rune(initials)
		initials = string(r[len(r)-2:])
	}
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40"><rect width="40" height="40" rx="4" fill="#8B9DC3"/><text x="50%%" y="55%%" dominant-baseline="middle" text-anchor="middle" fill="white" font-size="14" font-family="sans-serif">%s</text></svg>`,
		html.EscapeString(initials))
	return "data:image/svg+xml;charset=utf-8," + svgURIEncode(svg)
}

func svgURIEncode(s string) string {
	s = strings.ReplaceAll(s, `"`, "'")
	s = strings.ReplaceAll(s, "#", "%23")
	s = strings.ReplaceAll(s, "<", "%3C")
	s = strings.ReplaceAll(s, ">", "%3E")
	return s
}

func buildTitle(opts ExportOptions, chunk, from, to int, total int) string {
	kind := "好友"
	if opts.TableKind == "group" {
		kind = "群"
	}
	return fmt.Sprintf("QQ聊天记录 - %s%s - 第%d-%d条（共%d条）- 第%d块",
		kind, opts.TableID, from, to, total, chunk)
}

func sanitizeFilename(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

func formatTime(ts int64) string {
	t := time.Unix(ts, 0)
	return t.Format("2006/01/02 15:04:05")
}

// inlineCSS returns the CSS for the exported MHTML document.
func inlineCSS() string {
	return `
* { box-sizing: border-box; margin: 0; padding: 0; }
body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
  background: #ededed;
  color: #333;
  font-size: 14px;
}
.chat-header {
  background: #fff;
  padding: 16px 20px;
  border-bottom: 1px solid #ddd;
  position: sticky;
  top: 0;
  z-index: 10;
}
.chat-header h2 { font-size: 16px; font-weight: 600; color: #222; }
.meta { font-size: 12px; color: #999; margin-top: 4px; }
.messages {
  max-width: 860px;
  margin: 0 auto;
  padding: 16px 12px;
}
.time-divider {
  text-align: center;
  margin: 12px 0;
}
.time-divider span {
  background: rgba(0,0,0,.08);
  color: #888;
  font-size: 11px;
  padding: 2px 10px;
  border-radius: 10px;
}
.msg-row {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin: 8px 0;
}
.msg-row.right {
  flex-direction: row-reverse;
}
.msg-avatar {
  width: 40px;
  height: 40px;
  border-radius: 4px;
  object-fit: cover;
  flex-shrink: 0;
}
.msg-content {
  max-width: 65%;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.msg-row.right .msg-content {
  align-items: flex-end;
}
.msg-nick {
  font-size: 12px;
  color: #888;
}
.msg-row.right .msg-nick {
  text-align: right;
}
.msg-bubble {
  background: #fff;
  border-radius: 4px;
  padding: 8px 12px;
  line-height: 1.5;
  word-break: break-word;
  box-shadow: 0 1px 2px rgba(0,0,0,.08);
}
.msg-row.right .msg-bubble {
  background: #95ec69;
}
.msg-time {
  font-size: 11px;
  color: #bbb;
}
.qq-face {
  width: 22px;
  height: 22px;
  vertical-align: middle;
}
.face-badge {
  display: inline-block;
  background: #f0f0f0;
  border-radius: 3px;
  padding: 1px 4px;
  font-size: 12px;
  color: #666;
  vertical-align: middle;
}
.chat-img {
  max-width: 240px;
  max-height: 240px;
  border-radius: 4px;
  cursor: pointer;
  display: block;
  margin: 4px 0;
}
.img-missing {
  display: inline-block;
  width: 32px;
  height: 32px;
  background: #f0f0f0;
  border: 1px solid #ddd;
  border-radius: 4px;
  text-align: center;
  line-height: 32px;
  color: #aaa;
  font-size: 16px;
}
`
}
