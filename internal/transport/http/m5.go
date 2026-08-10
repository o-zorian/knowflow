package transporthttp

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"knowflow/internal/auth"
)

func registerM5Routes(mux *http.ServeMux, logger *slog.Logger, services BusinessServices) {
	requireAdmin := func(handler http.HandlerFunc) http.HandlerFunc {
		return authenticate(services.Auth, services.RateLimiter, logger, func(w http.ResponseWriter, r *http.Request) {
			if currentUser(r).Role != auth.RoleAdmin {
				WriteError(w, r, http.StatusForbidden, "ADMIN_REQUIRED", "administrator access is required")
				return
			}
			handler(w, r)
		})
	}
	mux.HandleFunc("GET /api/v1/admin/metrics/summary", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		result, err := services.Governance.Summary(r.Context())
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, result)
	}))
	mux.HandleFunc("GET /api/v1/admin/ingestion-jobs", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		page, size, ok := pageParams(w, r)
		if !ok {
			return
		}
		items, total, err := services.Governance.IngestionJobs(r.Context(), page, size, r.URL.Query().Get("status"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "total": total})
	}))
	mux.HandleFunc("GET /api/v1/admin/model-usage", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		page, size, ok := pageParams(w, r)
		if !ok {
			return
		}
		items, total, err := services.Governance.ModelUsage(r.Context(), page, size)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "total": total})
	}))
	mux.HandleFunc("GET /api/v1/admin/users", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		page, size, ok := pageParams(w, r)
		if !ok {
			return
		}
		items, total, err := services.Governance.Users(r.Context(), page, size)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]any{"items": items, "page": page, "page_size": size, "total": total})
	}))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Status string `json:"status"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Status = strings.TrimSpace(input.Status)
		if input.Status != "active" && input.Status != "disabled" {
			WriteError(w, r, http.StatusBadRequest, "INVALID_USER_STATUS", "status must be active or disabled")
			return
		}
		err := services.Governance.SetUserStatus(r.Context(), r.PathValue("id"), input.Status)
		if errors.Is(err, pgx.ErrNoRows) {
			WriteError(w, r, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
			return
		}
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		WriteSuccess(w, r, http.StatusOK, map[string]string{"status": input.Status})
	}))
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminPage))
	})
}

const adminPage = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>KnowFlow 管理治理</title><style>
body{font:14px system-ui;margin:0;background:#f5f7fb;color:#172033}main{max-width:1100px;margin:32px auto;padding:0 20px}h1{font-size:24px}.bar,.card{background:white;padding:16px;border-radius:10px;margin:12px 0;box-shadow:0 1px 4px #ccd3df}input{width:70%;padding:9px}button{padding:9px 14px}#cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:10px}.metric{background:#eef3ff;padding:12px;border-radius:8px}.metric b{display:block;font-size:22px;margin-top:5px}table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:8px;border-bottom:1px solid #e5e8ef}small{color:#64748b}.error{color:#b42318}</style></head><body><main>
<h1>KnowFlow 评测与治理</h1><div class="bar"><label>管理员 Access Token　</label><input id="token" type="password" placeholder="Bearer token（仅保存在当前页面内存）"><button onclick="loadAll()">刷新</button><span id="error" class="error"></span></div>
<section class="card"><h2>运行概况</h2><div id="cards"></div></section><section class="card"><h2>最近失败的索引任务</h2><table><thead><tr><th>文件</th><th>阶段</th><th>尝试</th><th>错误</th><th>时间</th></tr></thead><tbody id="jobs"></tbody></table></section>
<section class="card"><h2>模型调用明细</h2><table><thead><tr><th>类型 / 模型</th><th>状态</th><th>Token / 文本</th><th>延迟</th><th>成本</th><th>时间</th></tr></thead><tbody id="usage"></tbody></table></section>
<small>数据来自真实管理 API；Prometheus 指标位于 <a href="/metrics">/metrics</a>。</small></main><script>
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
async function api(path){const r=await fetch(path,{headers:{Authorization:'Bearer '+document.getElementById('token').value}});const v=await r.json();if(!r.ok)throw Error(v.error?.message||r.status);return v.data}
async function loadAll(){error.textContent='';try{const [s,j,u]=await Promise.all([api('/api/v1/admin/metrics/summary'),api('/api/v1/admin/ingestion-jobs?page_size=10'),api('/api/v1/admin/model-usage?page_size=10')]);cards.innerHTML=Object.entries(s).map(([k,v])=>'<div class="metric">'+esc(k)+'<b>'+esc(typeof v==='number'?Math.round(v*10000)/10000:v)+'</b></div>').join('');jobs.innerHTML=j.items.map(x=>'<tr><td>'+esc(x.filename)+'</td><td>'+esc(x.stage)+'</td><td>'+esc(x.attempts)+'</td><td>'+esc(x.error_code||'')+'</td><td>'+esc(x.created_at)+'</td></tr>').join('');usage.innerHTML=u.items.map(x=>'<tr><td>'+esc(x.request_type)+' / '+esc(x.model)+'</td><td>'+esc(x.status)+'</td><td>'+esc(x.prompt_tokens+x.completion_tokens)+' / '+esc(x.text_count)+'</td><td>'+esc(x.latency_ms)+' ms</td><td>$'+esc(x.estimated_cost_usd)+'</td><td>'+esc(x.created_at)+'</td></tr>').join('')}catch(e){error.textContent='　'+e.message}}
</script></body></html>`
