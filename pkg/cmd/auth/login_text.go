package auth

import (
	"os"

	"github.com/sepayvn/sepay-cli/internal/locale"
	"github.com/sepayvn/sepay-cli/internal/prompt"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

// loginText is the wording of the login screen, the one screen shown in the user's language;
// help, flags, --json and the remaining errors stay English.
type loginText struct {
	code, opening, visit, notOpening string
	wait                             prompt.DeviceWaitText
	loggedIn, addAccount             string
	denied, expired                  string
}

var loginTextEN = loginText{
	code:       "Open your browser to authenticate. Your code: %s\n",
	opening:    "Opening %s\n",
	visit:      "Visit: %s\n",
	notOpening: "! not opening a browser: %v\n",
	wait: prompt.DeviceWaitText{
		CopyLink: "copy link", CopyCode: "copy code", OpenBrowser: "open browser", Cancel: "cancel",
		Waiting:    "waiting for approval…",
		LinkCopied: "link sent to the clipboard", CodeCopied: "code sent to the clipboard",
		Opened: "opened the browser", NotOpened: "could not open a browser; copy the link instead",
	},
	loggedIn:   "%s Logged in as %s\n",
	addAccount: "add another account with: sepay login --profile <name>",
	denied:     "authorization denied",
	expired:    "the login code expired, run: sepay login",
}

var loginTextVI = loginText{
	code:       "Mở trình duyệt để xác thực. Mã của bạn: %s\n",
	opening:    "Đang mở %s\n",
	visit:      "Truy cập: %s\n",
	notOpening: "! không mở trình duyệt: %v\n",
	wait: prompt.DeviceWaitText{
		CopyLink: "sao chép link", CopyCode: "sao chép mã", OpenBrowser: "mở trình duyệt", Cancel: "huỷ",
		Waiting:    "đang chờ duyệt…",
		LinkCopied: "đã gửi link vào clipboard", CodeCopied: "đã gửi mã vào clipboard",
		Opened: "đã mở trình duyệt", NotOpened: "không mở được trình duyệt, hãy sao chép link",
	},
	loggedIn:   "%s Đã đăng nhập: %s\n",
	addAccount: "thêm tài khoản khác: sepay login --profile <name>",
	denied:     "yêu cầu đăng nhập đã bị từ chối",
	expired:    "mã đăng nhập đã hết hạn, chạy lại: sepay login",
}

// currentLoginText keeps English under --json and -o yaml, whose error object a script parses.
func currentLoginText(f *cmdutil.Factory) loginText {
	if f.Global.Output != "json" && f.Global.Output != "yaml" && locale.Detect(os.Getenv) == locale.VI {
		return loginTextVI
	}
	return loginTextEN
}
