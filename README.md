[Tiếng Việt](README.md) · [English](README.en.md)

# sepay-cli

[![CI](https://github.com/sepayvn/sepay-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/sepayvn/sepay-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sepayvn/sepay-cli)](https://github.com/sepayvn/sepay-cli/releases/latest)
[![npm](https://img.shields.io/npm/v/sepay-cli)](https://www.npmjs.com/package/sepay-cli)
[![Go Reference](https://pkg.go.dev/badge/github.com/sepayvn/sepay-cli.svg)](https://pkg.go.dev/github.com/sepayvn/sepay-cli)
[![License](https://img.shields.io/github/license/sepayvn/sepay-cli)](LICENSE)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/sepayvn/sepay-cli/badge)](https://scorecard.dev/viewer/?uri=github.com/sepayvn/sepay-cli)

SePay là công ty hạ tầng Open Banking hàng đầu tại Việt Nam, cung cấp API ngân hàng và tự động hoá dòng tiền cho doanh nghiệp, từ thu tiền, đối soát, thông báo đến xuất hoá đơn.
Tạo tài khoản tại [my.sepay.vn/register](https://my.sepay.vn/register), rồi bật [Test mode](https://developer.sepay.vn/vi/tien-ich-khac/test-mode) (sandbox) cho công ty.
Sandbox miễn phí và không đụng đến tiền thật.

`sepay` là công cụ dòng lệnh của SePay. Đăng nhập, xem giao dịch, tạo webhook và nhận webhook ngay
trên `localhost`, tạo đơn hàng thanh toán hosted checkout, và giả lập toàn bộ các bước đó trên sandbox.

```
sepay <command> <subcommand> [flags]
```

## Cài đặt

| Kênh | Lệnh |
|---|---|
| Homebrew (macOS, Linux) | `brew install sepayvn/sepay-cli/sepay` |
| Scoop (Windows) | `scoop bucket add sepay https://github.com/sepayvn/scoop-sepay-cli` rồi `scoop install sepay/sepay` |
| Script (macOS, Linux) | `curl -fsSL https://get.sepay.vn/sepay-cli-install.sh \| sh` |
| npm | `npm i -g sepay-cli` |
| Go | `go install github.com/sepayvn/sepay-cli/cmd/sepay@latest` |
| Tải thủ công | archive cho từng nền tảng ở [GitHub Releases](https://github.com/sepayvn/sepay-cli/releases) |

Script cài đặt kiểm tra SHA-256 của archive với `checksums.txt`, và kiểm tra thêm chữ ký cosign
của `checksums.txt` khi máy có `cosign`; `SEPAY_REQUIRE_SIGNATURE=1` từ chối cài khi không có
`cosign`. `SEPAY_VERSION` (hoặc `--version`) chọn phiên bản, `SEPAY_INSTALL_DIR` (hoặc `--dir`)
chọn thư mục cài (mặc định `$HOME/.local/bin`).

Ghim phiên bản trong một project Node:

```bash
npm install --save-dev sepay-cli@<version>
npx sepay-cli version
npx sepay-cli@next version                    # bản release candidate mới nhất
```

Các gói nền tảng của npm nằm dưới org `@sepayjsc` (`@sepayjsc/cli-*`). `@sepayjsc` là npm org của
Công ty CP SePay (SePay JSC); mã nguồn ở [github.com/sepayvn](https://github.com/sepayvn).

### Xác minh bản tải về

Mỗi GitHub Release có `checksums.txt`, chữ ký `checksums.txt.sig` và chứng chỉ `checksums.txt.pem`.
Tải chúng về cùng thư mục với archive, rồi:

```bash
cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/sepayvn/sepay-cli/\.github/workflows/release\.yml@refs/(heads/main|tags/v.*)$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum -c --ignore-missing checksums.txt      # macOS: shasum -a 256 -c --ignore-missing checksums.txt
gh attestation verify sepay_<version>_linux_amd64.tar.gz -R sepayvn/sepay-cli
```

Lệnh đầu chứng minh `checksums.txt` do workflow release của repo này ký. Lệnh thứ hai chứng minh
archive khớp file đó. Lệnh cuối kiểm tra build provenance: archive được build từ commit và workflow nào.

### Cập nhật

```bash
npm install -g sepay-cli@latest      # cài toàn cục bằng npm
npm install -D sepay-cli@latest      # ghim trong project (hoặc để Dependabot / Renovate mở PR)
brew upgrade sepay                   # Homebrew
scoop update sepay                   # Scoop
```

Cài bằng script hoặc `go install` thì chạy lại đúng lệnh đã cài.

Mỗi ngày một lần, khi bạn chạy lệnh trên terminal, CLI kiểm tra xem đã có bản mới chưa và bản
đang dùng có bị ngừng hỗ trợ (bị thu hồi hoặc có lỗ hổng) không, rồi in một dòng thông báo ngắn ra
stderr sau khi lệnh chạy xong. CLI không bao giờ tự cài gì, và không thông báo khi chạy trong CI,
khi dùng `--json`, hoặc khi đặt `SEPAY_NO_UPDATE_NOTIFIER=1`.

## Bắt đầu

```bash
sepay login          # mở trình duyệt để xác nhận một mã dùng một lần
sepay auth status    # tài khoản và công ty bạn đang dùng
```

Token được lưu trong keychain của hệ điều hành. Trên máy không có keychain dùng được (server, máy
truy cập qua SSH), CLI tự lưu token vào một file chỉ tài khoản của bạn đọc được và in cảnh báo màu
vàng; bạn không cần làm gì thêm. Muốn lưu vào file ngay từ đầu: `sepay login --insecure-storage`.
Máy không có trình duyệt: `sepay login --with-token`.

Mọi lệnh mặc định chạy trên **sandbox**. Thêm `--env production` để làm việc với dữ liệu thật.

## Nhận webhook đầu tiên trên localhost

Không cần URL công khai, không cần tunnel.

```bash
# Handler của bạn đang chạy ở localhost:3000

sepay sandbox init --with-va --with-webhook-listen        # tạo tài khoản sandbox + webhook
sepay webhook listen --forward localhost:3000 --print     # chuyển sự kiện về handler

# Mở terminal thứ hai: tạo một giao dịch
sepay bank-account list                                   # ghi lại xid
sepay sandbox simulate payment_in --bank-account <ba_xid> --amount 50000 --code ABC123
```

Mỗi sự kiện mang đúng body và các header chữ ký mà SePay gửi, nên handler chạy đúng luồng thật,
kể cả bước tự kiểm tra chữ ký. Kiểm tra handler phản hồi đúng: `sepay webhook test --forward localhost:3000 --amount 50000`.

## Các lệnh

| Lệnh | Chức năng |
|---|---|
| `login`, `logout` | Đăng nhập, đăng xuất |
| `auth` | `status`, `switch`, `refresh`, `sessions`, `revoke`, `token` |
| `transaction` | Xem danh sách và chi tiết giao dịch |
| `bank-account` | Quản lý tài khoản ngân hàng |
| `webhook` | Tạo, sửa, xem log, `listen`, `test`, `replay` |
| `va` | Quản lý tài khoản ảo (VA) |
| `paycode` | Tiền tố mã thanh toán |
| `pg` | Đơn hàng và merchant của Cổng thanh toán (Payment Gateway) |
| `bankhub` | Công ty, tài khoản và link token của Bank Hub |
| `einvoice` | Hóa đơn điện tử |
| `apikey` | API key |
| `quota` | Hạn mức gói và mức sử dụng |
| `sandbox` | `init`, `seed`, `simulate`, `reset` (chỉ trên sandbox) |
| `api` | Gọi bất kỳ endpoint nào của SePay API bằng token đã lưu |
| `config` | Cấu hình CLI |
| `completion` | Sinh script tự hoàn thành lệnh cho shell |
| `version` | Phiên bản và thông tin build |

`sepay --help` liệt kê toàn bộ. `sepay --map=paths` in mọi lệnh; `--map=json` kèm thêm metadata
cho máy đọc.

## Flag dùng chung

| Flag | Ý nghĩa |
|---|---|
| `--env sandbox\|production` | Môi trường, mặc định `sandbox` |
| `--json`, `-o json\|yaml` | Output cho máy đọc, không màu; mọi lệnh đọc dữ liệu đều hỗ trợ |
| `-p, --profile` | Tài khoản cần dùng, khi đang đăng nhập nhiều tài khoản |
| `-y, --yes` | Bỏ bước xác nhận ở lệnh ghi (script, CI); không bỏ qua được OTP |
| `--no-input` | Không bao giờ hỏi; thiếu giá trị thì báo lỗi |
| `--idempotency-key` | Chạy lại một lệnh ghi bị gián đoạn mà không thực hiện hai lần |
| `-v, --verbose` | Log gỡ lỗi; token và OTP luôn được che |

Biến môi trường: `SEPAY_PROFILE`, `SEPAY_CONFIG_DIR`, `SEPAY_NO_UPDATE_NOTIFIER`, `SEPAY_LANG`, `NO_COLOR`. Chi tiết: `sepay help environment`.

## Mã thoát

| Mã | Ý nghĩa |
|---|---|
| 0 | Thành công |
| 1 | Lỗi phía server, lỗi mạng, hoặc dữ liệu bị từ chối; đọc `error_code` trước khi chạy lại |
| 2 | Sai flag, sai tham số, lệnh không tồn tại |
| 3 | Lệnh có trong CLI nhưng chưa có backend; đừng chạy lại |
| 4 | Chưa đăng nhập: `sepay login` |
| 5 | Token thiếu quyền (scope) cần thiết: `sepay auth refresh --scopes <scope>` |
| 6 | Handler trên máy bạn không nhận sự kiện (`webhook test`) |
| 7 | Server yêu cầu OTP; hãy thực hiện thao tác này trên web |
| 8 | Tài khoản hoặc công ty của bạn không được phép làm việc này |
| 130 | Bị ngắt: Ctrl+C hoặc Esc khi đang hỏi, hoặc tín hiệu dừng |

Lỗi in ra `stderr`, kết quả in ra `stdout`. Khi dùng `--json`, lỗi cũng ở dạng JSON, kèm
`error_code` và `exit_code`. Danh sách đầy đủ: `sepay help exit-codes`.

## Tự hoàn thành lệnh (shell completion)

Cài bằng Homebrew thì script bash, zsh, fish đã có sẵn. Cài từ gói release hoặc npm thì
ghi script ra file một lần; shell nạp từ file nên `sepay` không chạy mỗi lần mở shell.

```bash
# Bash (cần gói bash-completion)
mkdir -p ~/.local/share/bash-completion/completions
sepay completion bash > ~/.local/share/bash-completion/completions/sepay

# Zsh (rồi thêm fpath+=~/.zfunc vào ~/.zshrc, trước compinit)
mkdir -p ~/.zfunc
sepay completion zsh > ~/.zfunc/_sepay

# Fish
sepay completion fish > ~/.config/fish/completions/sepay.fish

# PowerShell (thêm dòng này vào $PROFILE)
sepay completion powershell | Out-String | Invoke-Expression
```

## Tài liệu

Hướng dẫn theo tác vụ, body của webhook và IPN, tài liệu API:
[developer.sepay.vn/vi/agents/cli](https://developer.sepay.vn/vi/agents/cli).

Ngay trong terminal, `sepay help <topic>` in các topic `environment`, `errors`, `exit-codes` và
`glossary` đúng với phiên bản đang chạy.

## Go API

Module này là một CLI, không phải thư viện. Các package dưới `pkg/` theo bố cục của
[cli/cli](https://github.com/cli/cli) và không có cam kết tương thích nào giữa các phiên bản.

## Đóng góp

[CONTRIBUTING.md](CONTRIBUTING.md). Báo cáo lỗ hổng bảo mật: [SECURITY.md](SECURITY.md).

## Giấy phép

[Apache-2.0](LICENSE) © SePay JSC.
