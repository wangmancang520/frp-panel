package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"frp-panel/internal/auth"
	"frp-panel/internal/store"
)

func (a *App) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		MaxAge: maxAge, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	if ok, wait := a.lim.Allowed(ip); !ok {
		writeErr(w, 429, fmt.Sprintf("失败次数过多，请 %d 分钟后再试", int(wait.Minutes())+1))
		return
	}
	admin := a.St.Get().Admin
	ok := false
	if auth.ConstantEq(req.Username, admin.Username) {
		ok = auth.CheckPassword(admin.PasswordHash, req.Password)
	} else {
		auth.CheckDummy(req.Password)
	}
	if !ok {
		a.lim.Fail(ip)
		time.Sleep(300 * time.Millisecond)
		writeErr(w, 401, "用户名或密码错误")
		return
	}
	a.lim.Reset(ip)
	a.setSessionCookie(w, a.sess.Create(admin.Username), int(auth.SessionTTL.Seconds()))
	writeJSON(w, 200, map[string]any{"ok": true, "mustChange": admin.MustChange})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if t, ok := r.Context().Value(ctxKey{}).(string); ok {
		a.sess.Delete(t)
	}
	a.setSessionCookie(w, "", -1)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	c := a.St.Get()
	writeJSON(w, 200, map[string]any{"username": c.Admin.Username, "role": c.Role, "mustChange": c.Admin.MustChange})
}

func PasswordProblem(p string) string {
	if len(p) < 10 {
		return "密码至少 10 位"
	}
	var lower, upper, digit, other bool
	for _, r := range p {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	if n < 3 {
		return "密码需包含小写、大写、数字、符号中的至少三种"
	}
	return ""
}

func (a *App) handlePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old      string `json:"old"`
		New      string `json:"new"`
		Username string `json:"username"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	if ok, _ := a.lim.Allowed(ip); !ok {
		writeErr(w, 429, "失败次数过多，请稍后再试")
		return
	}
	cur := a.St.Get().Admin
	if !auth.CheckPassword(cur.PasswordHash, req.Old) {
		a.lim.Fail(ip)
		writeErr(w, 400, "当前密码不正确")
		return
	}
	if req.New == req.Old {
		writeErr(w, 400, "新密码不能与当前密码相同")
		return
	}
	if p := PasswordProblem(req.New); p != "" {
		writeErr(w, 400, p)
		return
	}
	user := strings.TrimSpace(req.Username)
	if user == "" {
		user = cur.Username
	}
	if len(user) > 32 || strings.ContainsAny(user, " \t\r\n") {
		writeErr(w, 400, "用户名无效")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := a.St.Update(func(c *store.Config) {
		c.Admin = store.Admin{Username: user, PasswordHash: hash, MustChange: false}
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	keep, _ := r.Context().Value(ctxKey{}).(string)
	a.sess.DeleteAllExcept(keep)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
