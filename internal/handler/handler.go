package handler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"qqviewer/internal/avatar"
	"qqviewer/internal/config"
	"qqviewer/internal/db"
	"qqviewer/internal/exporter"
	"qqviewer/internal/parser"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Handler holds all dependencies for HTTP handlers.
type Handler struct {
	cfg   *config.Config
	db    *db.DB
	cache *avatar.Cache
	tmpls *template.Template
}

// New creates a new Handler.
func New(cfg *config.Config, database *db.DB, cache *avatar.Cache, tmpls *template.Template) *Handler {
	return &Handler{cfg: cfg, db: database, cache: cache, tmpls: tmpls}
}

// Router builds and returns the chi router.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Static files
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Pages — SPA: all navigation is handled client-side
	r.Get("/", h.indexPage)
	// Legacy /chat/{table} redirect to SPA root
	r.Get("/chat/{table}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})

	// API
	r.Get("/api/tables", h.apiTables)
	r.Get("/api/messages/{table}", h.apiMessages)
	r.Get("/api/senders/{table}", h.apiSenders)
	r.Get("/api/timerange/{table}", h.apiTimeRange)
	r.Get("/api/avatar/{uin}", h.apiAvatar)
	r.Get("/api/groupavatar/{uin}", h.apiGroupAvatar)
	r.Get("/api/nickname/{uin}", h.apiNickname)
	r.Get("/api/userimg/*", h.apiUserImg)
	r.Get("/api/config", h.apiGetConfig)
	r.Post("/api/config", h.apiPostConfig)
	// Export
	r.Get("/api/export/page/{table}", h.apiExportPage)
	r.Post("/api/export/bulk/{table}", h.apiExportBulk)

	return r
}

// ---- Page handlers ----

func (h *Handler) indexPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Config": h.cfg,
	}
	if err := h.tmpls.ExecuteTemplate(w, "index.html", data); err != nil {
		log.Printf("index template: %v", err)
	}
}

func (h *Handler) chatPage(w http.ResponseWriter, r *http.Request) {
	table := chi.URLParam(r, "table")
	if _, ok := h.db.FindTable(table); !ok {
		http.Error(w, "table not found", 404)
		return
	}
	minT, maxT, _ := h.db.GetTimeRange(table)
	data := map[string]interface{}{
		"Table":  table,
		"Config": h.cfg,
		"MinT":   minT,
		"MaxT":   maxT,
	}
	if err := h.tmpls.ExecuteTemplate(w, "chat.html", data); err != nil {
		log.Printf("chat template: %v", err)
	}
}

// ---- API handlers ----

func (h *Handler) apiTables(w http.ResponseWriter, r *http.Request) {
	tables := h.db.Tables()
	type TableResp struct {
		db.TableInfo
		Nickname string `json:"nickname"`
	}
	resp := make([]TableResp, len(tables))
	for i, t := range tables {
		nick := ""
		if t.Kind == "buddy" {
			var uin uint64
			fmt.Sscanf(t.ID, "%d", &uin)
			nick = h.cache.Nickname(uin)
			// Trigger prefetch for buddy avatar+nickname
			h.cache.Prefetch([]uint64{uin})
		} else if t.Kind == "group" {
			// Trigger prefetch for group avatar
			var uin uint64
			fmt.Sscanf(t.ID, "%d", &uin)
			h.cache.PrefetchGroup(uin)
		}
		resp[i] = TableResp{TableInfo: t, Nickname: nick}
	}
	jsonResp(w, resp)
}

func (h *Handler) apiMessages(w http.ResponseWriter, r *http.Request) {
	table := chi.URLParam(r, "table")
	if _, ok := h.db.FindTable(table); !ok {
		http.Error(w, "table not found", 404)
		return
	}

	q := r.URL.Query()
	pageSize := parseInt(q.Get("page_size"), 50)
	if pageSize < 1 || pageSize > 1000 {
		pageSize = 50
	}
	offset := parseInt(q.Get("offset"), 0)
	keyword := q.Get("keyword")
	anchorTime := parseInt64(q.Get("anchor_time"), 0)

	// Support multiple sender_uin values: ?sender_uin=100001&sender_uin=100002
	var senderUins []uint64
	for _, s := range q["sender_uin"] {
		if u := parseUint64(s, 0); u > 0 {
			senderUins = append(senderUins, u)
		}
	}

	result, err := h.db.QueryMessages(table, offset, pageSize, keyword, senderUins, anchorTime)
	if err != nil {
		log.Printf("query messages: %v", err)
		http.Error(w, "query error", 500)
		return
	}

	type MsgResp struct {
		db.Message
		HTML     string `json:"html"`
		Nickname string `json:"nickname"`
	}
	msgs := make([]MsgResp, len(result.Messages))
	var uins []uint64
	seenUins := make(map[uint64]bool)
	for i, m := range result.Messages {
		htmlContent := parser.ParseDecodedMsg(m.DecodedMsg, h.cfg.Images.BaseDir, "/api/userimg/")
		nick := h.cache.Nickname(m.SenderUin)
		msgs[i] = MsgResp{Message: m, HTML: htmlContent, Nickname: nick}
		if !seenUins[m.SenderUin] {
			seenUins[m.SenderUin] = true
			uins = append(uins, m.SenderUin)
		}
	}
	h.cache.Prefetch(uins)

	type Resp struct {
		Messages  []MsgResp `json:"messages"`
		Total     int64     `json:"total"`
		HasPrev   bool      `json:"has_prev"`
		HasNext   bool      `json:"has_next"`
		FirstTime int64     `json:"first_time"`
		LastTime  int64     `json:"last_time"`
		Offset    int       `json:"offset"`
	}
	jsonResp(w, Resp{
		Messages:  msgs,
		Total:     result.Total,
		HasPrev:   result.HasPrev,
		HasNext:   result.HasNext,
		FirstTime: result.FirstTime,
		LastTime:  result.LastTime,
		Offset:    result.Offset, // use actual offset (may differ when anchorTime adjusts it)
	})
}

func (h *Handler) apiSenders(w http.ResponseWriter, r *http.Request) {
	table := chi.URLParam(r, "table")
	if _, ok := h.db.FindTable(table); !ok {
		http.Error(w, "table not found", 404)
		return
	}
	uins, err := h.db.GetSenders(table)
	if err != nil {
		http.Error(w, "query error", 500)
		return
	}
	type SenderResp struct {
		UIN      uint64 `json:"uin"`
		Nickname string `json:"nickname"`
	}
	resp := make([]SenderResp, len(uins))
	for i, u := range uins {
		resp[i] = SenderResp{UIN: u, Nickname: h.cache.Nickname(u)}
	}
	h.cache.Prefetch(uins)
	jsonResp(w, resp)
}

func (h *Handler) apiTimeRange(w http.ResponseWriter, r *http.Request) {
	table := chi.URLParam(r, "table")
	if _, ok := h.db.FindTable(table); !ok {
		http.Error(w, "table not found", 404)
		return
	}
	minT, maxT, err := h.db.GetTimeRange(table)
	if err != nil {
		http.Error(w, "query error", 500)
		return
	}
	jsonResp(w, map[string]int64{"min": minT, "max": maxT})
}

func (h *Handler) apiAvatar(w http.ResponseWriter, r *http.Request) {
	uinStr := chi.URLParam(r, "uin")
	uin, err := strconv.ParseUint(uinStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid uin", 400)
		return
	}
	if !h.cache.ServeAvatar(uin, w) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		w.Write(defaultAvatarSVG(uinStr))
	}
}

func (h *Handler) apiGroupAvatar(w http.ResponseWriter, r *http.Request) {
	// Group avatars are always served as SVG default icons (no API call).
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(200)
	w.Write(groupAvatarSVG())
}

func (h *Handler) apiNickname(w http.ResponseWriter, r *http.Request) {
	uinStr := chi.URLParam(r, "uin")
	uin, err := strconv.ParseUint(uinStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid uin", 400)
		return
	}
	nick := h.cache.Nickname(uin)
	// Return whether nickname has been resolved (even if empty), so frontend
	// knows not to keep polling.
	fetched := h.cache.NicknameFetched(uin)
	jsonResp(w, map[string]interface{}{
		"nickname": nick,
		"uin":      uinStr,
		"fetched":  fetched,
	})
}

func (h *Handler) apiUserImg(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Images.BaseDir == "" {
		http.Error(w, "image base dir not configured", 404)
		return
	}
	imgPath := chi.URLParam(r, "*")
	cleaned := filepath.Clean("/" + imgPath)
	if strings.Contains(cleaned, "..") {
		http.Error(w, "invalid path", 400)
		return
	}
	fullPath := filepath.Join(h.cfg.Images.BaseDir, cleaned)
	absBase, _ := filepath.Abs(h.cfg.Images.BaseDir)
	absPath, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absPath, absBase) {
		http.Error(w, "forbidden", 403)
		return
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	ext := strings.ToLower(filepath.Ext(fullPath))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(data)
}

// configJSON is a JSON-serializable version of Config with lowercase keys.
type configJSON struct {
	User   userJSON   `json:"user"`
	Images imagesJSON `json:"images"`
}

type userJSON struct {
	MyQQ          uint64 `json:"my_qq"`
	BubbleOnRight bool   `json:"bubble_on_right"`
}

type imagesJSON struct {
	BaseDir string `json:"base_dir"`
}

func (h *Handler) apiGetConfig(w http.ResponseWriter, r *http.Request) {
	resp := configJSON{
		User: userJSON{
			MyQQ:          h.cfg.User.MyQQ,
			BubbleOnRight: h.cfg.User.BubbleOnRight,
		},
		Images: imagesJSON{
			BaseDir: h.cfg.Images.BaseDir,
		},
	}
	jsonResp(w, resp)
}

func (h *Handler) apiPostConfig(w http.ResponseWriter, r *http.Request) {
	var newCfg configJSON
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	h.cfg.User.MyQQ = newCfg.User.MyQQ
	h.cfg.User.BubbleOnRight = newCfg.User.BubbleOnRight
	h.cfg.Images.BaseDir = newCfg.Images.BaseDir
	if err := config.Save("config.toml", h.cfg); err != nil {
		log.Printf("save config: %v", err)
	}
	jsonResp(w, map[string]string{"status": "ok"})
}

// ---- Export handlers ----

// apiExportPage exports the currently visible page of messages as an MHTML file.
// Query params: same as apiMessages (offset, page_size, keyword, sender_uin, anchor_time)
func (h *Handler) apiExportPage(w http.ResponseWriter, r *http.Request) {
	table := chi.URLParam(r, "table")
	tinfo, ok := h.db.FindTable(table)
	if !ok {
		http.Error(w, "table not found", 404)
		return
	}

	q := r.URL.Query()
	pageSize := parseInt(q.Get("page_size"), 50)
	if pageSize < 1 || pageSize > 1000 {
		pageSize = 50
	}
	offset := parseInt(q.Get("offset"), 0)
	keyword := q.Get("keyword")
	anchorTime := parseInt64(q.Get("anchor_time"), 0)

	var senderUins []uint64
	for _, s := range q["sender_uin"] {
		if u := parseUint64(s, 0); u > 0 {
			senderUins = append(senderUins, u)
		}
	}

	result, err := h.db.QueryMessages(table, offset, pageSize, keyword, senderUins, anchorTime)
	if err != nil {
		http.Error(w, "query error", 500)
		return
	}

	opts := exporter.ExportOptions{
		TableName:   table,
		TableKind:   tinfo.Kind,
		TableID:     tinfo.ID,
		MyQQ:        h.cfg.User.MyQQ,
		BubbleRight: h.cfg.User.BubbleOnRight,
		ImageBase:   h.cfg.Images.BaseDir,
	}

	title := fmt.Sprintf("QQ聊天记录 - %s%s - 第%d-%d条",
		map[string]string{"group": "群", "buddy": "好友"}[tinfo.Kind],
		tinfo.ID,
		result.Offset+1,
		result.Offset+len(result.Messages),
	)

	filename := fmt.Sprintf("chat_%s_%s.mhtml",
		tinfo.ID,
		time.Now().Format("20060102_150405"),
	)

	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	if err := exporter.WriteMHTML(w, result.Messages, h.cache, opts, title); err != nil {
		log.Printf("export page: %v", err)
	}
}

// bulkExportReq is the JSON body for bulk export.
type bulkExportReq struct {
	Keyword    string   `json:"keyword"`
	SenderUins []uint64 `json:"sender_uins"`
	TimeFrom   int64    `json:"time_from"`
	TimeTo     int64    `json:"time_to"`
	ChunkSize  int      `json:"chunk_size"`
}

// apiExportBulk exports all matching messages as a ZIP of MHTML files.
func (h *Handler) apiExportBulk(w http.ResponseWriter, r *http.Request) {
	table := chi.URLParam(r, "table")
	tinfo, ok := h.db.FindTable(table)
	if !ok {
		http.Error(w, "table not found", 404)
		return
	}

	var req bulkExportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	if req.ChunkSize <= 0 {
		req.ChunkSize = 500
	}
	if req.ChunkSize > 5000 {
		req.ChunkSize = 5000
	}

	opts := exporter.ExportOptions{
		TableName:   table,
		TableKind:   tinfo.Kind,
		TableID:     tinfo.ID,
		Keyword:     req.Keyword,
		SenderUins:  req.SenderUins,
		TimeFrom:    req.TimeFrom,
		TimeTo:      req.TimeTo,
		ChunkSize:   req.ChunkSize,
		MyQQ:        h.cfg.User.MyQQ,
		BubbleRight: h.cfg.User.BubbleOnRight,
		ImageBase:   h.cfg.Images.BaseDir,
	}

	filename := fmt.Sprintf("chat_%s_%s.zip",
		tinfo.ID,
		time.Now().Format("20060102_150405"),
	)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	if err := exporter.BulkExportToZip(w, h.db, h.cache, opts); err != nil {
		log.Printf("export bulk: %v", err)
	}
}

// ---- Helpers ----

func jsonResp(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("json encode: %v", err)
	}
}

func parseInt(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

func parseUint64(s string, def uint64) uint64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}

func groupAvatarSVG() []byte {
	// A simple group icon: circle with two person silhouettes
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
  <rect width="40" height="40" rx="8" fill="#5B9BD5"/>
  <!-- person left -->
  <circle cx="14" cy="14" r="5" fill="white" opacity="0.9"/>
  <path d="M4 32 Q4 22 14 22 Q24 22 24 32" fill="white" opacity="0.9"/>
  <!-- person right (smaller, behind) -->
  <circle cx="26" cy="13" r="4" fill="white" opacity="0.6"/>
  <path d="M18 31 Q18 22 26 22 Q34 22 34 31" fill="white" opacity="0.6"/>
</svg>`
	return []byte(svg)
}

func defaultAvatarSVG(label string) []byte {
	initials := label
	if len(initials) > 2 {
		initials = initials[len(initials)-2:]
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40" viewBox="0 0 40 40">
  <rect width="40" height="40" rx="4" fill="#8B9DC3"/>
  <text x="50%" y="55%" dominant-baseline="middle" text-anchor="middle" fill="white" font-size="14" font-family="sans-serif">` + initials + `</text>
</svg>`
	return []byte(svg)
}
