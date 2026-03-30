package parser

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// ParseDecodedMsg parses a DecodedMsg string into an HTML string.
// imageBaseDir is the parent directory for user images.
// imageServePrefix is the URL prefix used to serve images (e.g. "/api/userimg/").
func ParseDecodedMsg(raw, imageBaseDir, imageServePrefix string) string {
	if raw == "" {
		return ""
	}

	var sb strings.Builder
	i := 0
	n := len(raw)

	for i < n {
		// Look for "[t:" prefix
		idx := strings.Index(raw[i:], "[t:")
		if idx < 0 {
			// No more tags; write remaining text
			sb.WriteString(escapeText(raw[i:]))
			break
		}

		// Write text before the tag
		if idx > 0 {
			sb.WriteString(escapeText(raw[i : i+idx]))
		}

		tagStart := i + idx
		// tagStart points to '['
		// Find the matching ']' — for img tags the path may contain ']',
		// but the tag always ends with ",hash=<hex>]" so we look for the
		// last ']' that closes a valid tag structure.
		tagContent, tagEnd, ok := findTagEnd(raw, tagStart)
		if !ok {
			// Not a valid tag; emit the '[' literally and advance past it
			sb.WriteString(escapeText("["))
			i = tagStart + 1
			continue
		}

		// tagContent is everything between '[' and ']' exclusive, i.e. "t:face,id=5,name=未知"
		renderTag(&sb, tagContent, imageBaseDir, imageServePrefix)
		i = tagEnd + 1 // advance past ']'
	}

	return sb.String()
}

// findTagEnd finds the closing ']' for a tag starting at tagStart (which points to '[').
// For img tags, the path may contain ']', so we find the tag end by looking for
// the pattern ",hash=<hex>]" at the end, or for face tags just the first ']'.
// Returns (content between '[' and ']', index of ']', ok).
func findTagEnd(s string, tagStart int) (string, int, bool) {
	if tagStart >= len(s) || s[tagStart] != '[' {
		return "", 0, false
	}

	// Quick check: must start with "[t:"
	if tagStart+3 > len(s) || s[tagStart:tagStart+3] != "[t:" {
		return "", 0, false
	}

	// Determine tag type
	rest := s[tagStart+1:] // skip '['
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return "", 0, false
	}
	// e.g. "t:face,..." or "t:img,..."
	// tagType is between ':' and first ','
	afterColon := rest[colonIdx+1:]
	commaIdx := strings.Index(afterColon, ",")
	var tagType string
	if commaIdx >= 0 {
		tagType = afterColon[:commaIdx]
	} else {
		tagType = afterColon
	}

	if tagType == "img" {
		// For img tags: the tag ends with ",hash=<32 hex chars>]"
		// Find that pattern from the end
		return findImgTagEnd(s, tagStart)
	}

	// For other tags (face, etc.): find the first ']' after tagStart
	closeIdx := strings.Index(s[tagStart:], "]")
	if closeIdx < 0 {
		return "", 0, false
	}
	closePos := tagStart + closeIdx
	content := s[tagStart+1 : closePos]
	return content, closePos, true
}

// findImgTagEnd finds the end of an img tag, handling ']' in path values.
// The tag ends with ",hash=<hex>]" where hex is a 32-char MD5 hash.
func findImgTagEnd(s string, tagStart int) (string, int, bool) {
	// Search for ",hash=" followed by hex chars and ']'
	searchFrom := tagStart + 3 // skip "[t:"
	for {
		hashIdx := strings.Index(s[searchFrom:], ",hash=")
		if hashIdx < 0 {
			// No hash field found; fall back to first ']'
			closeIdx := strings.Index(s[tagStart:], "]")
			if closeIdx < 0 {
				return "", 0, false
			}
			closePos := tagStart + closeIdx
			return s[tagStart+1 : closePos], closePos, true
		}
		hashStart := searchFrom + hashIdx
		// hashStart points to ','
		// After ",hash=" we expect hex chars then ']'
		afterHash := s[hashStart+6:] // skip ",hash="
		hexEnd := 0
		for hexEnd < len(afterHash) && isHexChar(afterHash[hexEnd]) {
			hexEnd++
		}
		if hexEnd > 0 && hexEnd < len(afterHash) && afterHash[hexEnd] == ']' {
			closePos := hashStart + 6 + hexEnd
			content := s[tagStart+1 : closePos]
			return content, closePos, true
		}
		// This ",hash=" wasn't the right one; keep searching
		searchFrom = hashStart + 6
	}
}

func isHexChar(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// escapeText converts plain text to HTML, preserving newlines.
func escapeText(s string) string {
	escaped := html.EscapeString(s)
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	return escaped
}

// renderTag parses the tag content and writes the appropriate HTML.
// content is e.g. "t:face,id=5,name=未知" or "t:img,path=...,hash=..."
func renderTag(sb *strings.Builder, content, imageBaseDir, imageServePrefix string) {
	// content starts with "t:tagtype,"
	colonIdx := strings.Index(content, ":")
	if colonIdx < 0 {
		sb.WriteString(escapeText("[" + content + "]"))
		return
	}
	afterColon := content[colonIdx+1:]
	commaIdx := strings.Index(afterColon, ",")
	var tagType, fieldsStr string
	if commaIdx >= 0 {
		tagType = afterColon[:commaIdx]
		fieldsStr = afterColon[commaIdx+1:]
	} else {
		tagType = afterColon
		fieldsStr = ""
	}

	switch tagType {
	case "face":
		fields := parseFields(fieldsStr)
		sb.WriteString(renderFace(fields))
	case "img":
		fields := parseImgFields(fieldsStr)
		sb.WriteString(renderImg(fields, imageBaseDir, imageServePrefix))
	default:
		sb.WriteString(fmt.Sprintf(`<span class="unknown-tag">%s</span>`,
			html.EscapeString("["+content+"]")))
	}
}

// parseFields parses "key=value,key=value,..." for simple tags (no commas in values).
func parseFields(s string) map[string]string {
	fields := make(map[string]string)
	if s == "" {
		return fields
	}
	parts := strings.Split(s, ",")
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			fields[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return fields
}

// parseImgFields parses img tag fields where path may contain commas.
// Format: path=UserDataImage:...,hash=<hex>
// Strategy: find ",hash=" from the end, split there.
func parseImgFields(s string) map[string]string {
	fields := make(map[string]string)
	if s == "" {
		return fields
	}

	// Find the last ",hash=" occurrence
	hashIdx := strings.LastIndex(s, ",hash=")
	if hashIdx >= 0 {
		// Everything before hashIdx is the path field(s)
		pathPart := s[:hashIdx]
		hashPart := s[hashIdx+1:] // "hash=<hex>"

		// Parse path: "path=..."
		if strings.HasPrefix(pathPart, "path=") {
			fields["path"] = pathPart[5:]
		} else {
			// Might have other fields before path; find "path="
			pIdx := strings.Index(pathPart, "path=")
			if pIdx >= 0 {
				fields["path"] = pathPart[pIdx+5:]
			}
		}

		// Parse hash
		kv := strings.SplitN(hashPart, "=", 2)
		if len(kv) == 2 {
			fields["hash"] = kv[1]
		}
	} else {
		// No hash field; treat entire string as path
		if strings.HasPrefix(s, "path=") {
			fields["path"] = s[5:]
		}
	}
	return fields
}

// renderFace renders a QQ system emoji.
// Emoji images are served from /static/img/emoji/{id}.gif
func renderFace(fields map[string]string) string {
	id := fields["id"]
	name := fields["name"]
	if name == "" {
		name = "表情"
	}
	if id == "" {
		return fmt.Sprintf(`<span class="face-badge" title="%s">[%s]</span>`,
			html.EscapeString(name), html.EscapeString(name))
	}
	return fmt.Sprintf(
		`<img class="qq-face" src="/static/img/emoji/%s.gif" alt="%s" title="%s" `+
			`onerror="this.outerHTML='<span class=&quot;face-badge&quot; title=&quot;%s&quot;>[%s]</span>'">`,
		html.EscapeString(id),
		html.EscapeString(name),
		html.EscapeString(name),
		html.EscapeString(name),
		html.EscapeString(name),
	)
}

// ParseDecodedMsgForExport is like ParseDecodedMsg but embeds user images as base64 data URIs
// for use in self-contained MHTML export files (no server needed).
func ParseDecodedMsgForExport(raw, imageBaseDir string) string {
	if raw == "" {
		return ""
	}

	var sb strings.Builder
	i := 0
	n := len(raw)

	for i < n {
		idx := strings.Index(raw[i:], "[t:")
		if idx < 0 {
			sb.WriteString(escapeText(raw[i:]))
			break
		}
		if idx > 0 {
			sb.WriteString(escapeText(raw[i : i+idx]))
		}
		tagStart := i + idx
		tagContent, tagEnd, ok := findTagEnd(raw, tagStart)
		if !ok {
			sb.WriteString(escapeText("["))
			i = tagStart + 1
			continue
		}
		renderTagForExport(&sb, tagContent, imageBaseDir)
		i = tagEnd + 1
	}
	return sb.String()
}

// renderTagForExport is like renderTag but embeds images as base64 data URIs.
func renderTagForExport(sb *strings.Builder, content, imageBaseDir string) {
	colonIdx := strings.Index(content, ":")
	if colonIdx < 0 {
		sb.WriteString(escapeText("[" + content + "]"))
		return
	}
	afterColon := content[colonIdx+1:]
	commaIdx := strings.Index(afterColon, ",")
	var tagType, fieldsStr string
	if commaIdx >= 0 {
		tagType = afterColon[:commaIdx]
		fieldsStr = afterColon[commaIdx+1:]
	} else {
		tagType = afterColon
		fieldsStr = ""
	}
	switch tagType {
	case "face":
		fields := parseFields(fieldsStr)
		sb.WriteString(renderFaceForExport(fields))
	case "img":
		fields := parseImgFields(fieldsStr)
		sb.WriteString(renderImgForExport(fields, imageBaseDir))
	default:
		sb.WriteString(fmt.Sprintf(`<span class="unknown-tag">%s</span>`,
			html.EscapeString("["+content+"]")))
	}
}

// renderFaceForExport renders a face tag for export (no /static/ URL dependency).
func renderFaceForExport(fields map[string]string) string {
	name := fields["name"]
	if name == "" {
		name = "表情"
	}
	// In export we can't rely on /static/img/emoji/ being available, so always use badge.
	return fmt.Sprintf(`<span class="face-badge" title="%s">[%s]</span>`,
		html.EscapeString(name), html.EscapeString(name))
}

// renderImgForExport renders a user image as a base64 data URI for MHTML export.
func renderImgForExport(fields map[string]string, imageBaseDir string) string {
	path := fields["path"]
	if path == "" {
		return `<span class="img-missing" title="图片路径缺失">✕</span>`
	}
	cleanPath := path
	if idx := strings.Index(cleanPath, ":"); idx >= 0 {
		cleanPath = cleanPath[idx+1:]
	}
	urlPath := strings.ReplaceAll(cleanPath, `\`, `/`)
	fsPath := filepath.FromSlash(urlPath)

	if imageBaseDir != "" {
		fullPath := filepath.Join(imageBaseDir, fsPath)
		data, err := os.ReadFile(fullPath)
		if err == nil && len(data) > 0 {
			ct := sniffExportImageType(data)
			dataURI := fmt.Sprintf("data:%s;base64,%s", ct, encodeBase64(data))
			return fmt.Sprintf(
				`<img class="chat-img" src="%s" alt="图片" style="max-width:240px;max-height:240px;border-radius:4px;">`,
				dataURI,
			)
		}
	}
	return `<span class="img-missing" title="图片未找到">✕</span>`
}

func sniffExportImageType(data []byte) string {
	if len(data) < 4 {
		return "image/jpeg"
	}
	switch {
	case len(data) >= 4 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) >= 3 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
		return "image/gif"
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	default:
		return "image/jpeg"
	}
}

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// renderImg renders a user image tag.
func renderImg(fields map[string]string, imageBaseDir, imageServePrefix string) string {
	path := fields["path"]
	if path == "" {
		return `<span class="img-missing" title="图片路径缺失">✕</span>`
	}

	// Strip "UserDataImage:" or similar prefix (everything up to and including the first colon)
	cleanPath := path
	if idx := strings.Index(cleanPath, ":"); idx >= 0 {
		cleanPath = cleanPath[idx+1:]
	}
	// Normalize backslashes to forward slashes for URL building
	urlPath := strings.ReplaceAll(cleanPath, `\`, `/`)
	// For filesystem check, use OS-native separator
	fsPath := filepath.FromSlash(urlPath)

	// Check if the file exists on disk
	if imageBaseDir != "" {
		fullPath := filepath.Join(imageBaseDir, fsPath)
		if _, err := os.Stat(fullPath); err == nil {
			// Encode each path segment separately to preserve slashes
			segments := strings.Split(urlPath, "/")
			encodedSegments := make([]string, len(segments))
			for i, seg := range segments {
				encodedSegments[i] = url.PathEscape(seg)
			}
			encoded := strings.Join(encodedSegments, "/")
			imgURL := imageServePrefix + encoded
			return fmt.Sprintf(
				`<img class="chat-img" src="%s" alt="图片" loading="lazy" onclick="openLightbox(this.src)" `+
					`onerror="this.outerHTML='<span class=&quot;img-missing&quot; title=&quot;图片加载失败&quot;>✕</span>'">`,
				html.EscapeString(imgURL),
			)
		}
	}

	// File not found or no base dir configured — show ✕ without exposing path info
	return `<span class="img-missing" title="图片未找到">✕</span>`
}
