package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/robertji666/RemoteBrowser/internal/store"
)

func TestActionErrorMessageTranslatesStoreErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"quota", store.ErrQuotaExceeded, "已达到浏览器实例数量上限。停止的实例也占用名额，请删除不再使用的实例，或联系管理员增加配额。"},
		{"email", store.ErrEmailExists, "该邮箱已存在，请使用其他邮箱，或在用户列表中管理现有账号。"},
		{"conflict", store.ErrConflict, "记录已发生变化，请刷新页面后重试。"},
		{"inactive", store.ErrUserInactive, "该账号当前不可用（已禁用或正在删除），请联系管理员。"},
		{"protected", store.ErrUserProtected, "管理员账号受保护，不能执行此操作。"},
		{"instances", store.ErrUserHasInstances, "该用户仍有关联的浏览器实例，尚不能完成账号删除。请在用户管理中查看清理进度并重试。"},
		{"missing", store.ErrNotFound, "目标记录不存在或已删除，请刷新页面后重试。"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{tc.err, fmt.Errorf("operation failed: %w", tc.err), errors.Join(errors.New("cleanup pending"), tc.err)} {
				if got := actionErrorMessage(err); got != tc.want {
					t.Fatalf("got %q want %q", got, tc.want)
				}
			}
		})
	}
	custom := errors.New("两次输入的新密码不一致")
	if got := actionErrorMessage(custom); got != custom.Error() {
		t.Fatalf("custom explanation changed: %q", got)
	}
}

func TestActionErrorJSONKeepsStatusAndOriginalError(t *testing.T) {
	err := fmt.Errorf("create instance: %w", store.ErrQuotaExceeded)
	r := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	(&Handler{}).actionError(w, r, err)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status changed: %d", w.Code)
	}
	var body map[string]string
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body["error"] != err.Error() {
		t.Fatalf("original error changed: %q", body["error"])
	}
	if body["message"] != actionErrorMessage(err) {
		t.Fatalf("friendly message missing: %v", body)
	}
}
