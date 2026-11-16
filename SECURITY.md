# Chính sách bảo mật

[English below](#security-policy)

## Báo cáo lỗ hổng

Không mở issue công khai cho bất cứ điều gì nhạy cảm về bảo mật.

- **Kênh chính:** [GitHub Private Vulnerability Reporting](https://github.com/sepayvn/sepay-cli/security/advisories/new).
- **Dự phòng:** email **support@sepay.vn**.

Gửi kèm các bước tái hiện và phiên bản bị ảnh hưởng (`sepay version`).

## Phạm vi

| Trong phạm vi | Ngoài phạm vi |
|---|---|
| Binary `sepay` (mọi kênh cài đặt) | `api.sepay.vn` và các API của SePay |
| Script cài đặt `scripts/install.sh`, `scripts/install.ps1` | `my.sepay.vn` và các ứng dụng web khác của SePay |
| Các gói npm (`sepay-cli`, `@sepayjsc/cli-*`) | |
| Bộ kiểm tra phiên bản mới (update notifier) | |

## Thời gian phản hồi

| Bước | Thời hạn |
|---|---|
| Xác nhận đã nhận | 3 ngày làm việc |
| Đánh giá mức độ và phạm vi | 10 ngày |
| Công bố phối hợp | 90 ngày kể từ khi nhận báo cáo |

## Các phiên bản được hỗ trợ

Trong giai đoạn trước 1.0, chỉ bản release mới nhất nhận bản sửa.

| Phiên bản | Được hỗ trợ |
|---|---|
| 0.x mới nhất | có |
| 0.x cũ hơn | không, hãy nâng cấp |

## Cách xử lý một phiên bản có lỗ hổng

1. Xác định dải phiên bản bị ảnh hưởng (`git tag --contains <commit that introduced it>`).
2. Vấn đề nghiêm trọng: các bản cài mới được chuyển ngay sang một phiên bản an toàn (dist-tag
   `latest`) trong lúc chuẩn bị bản sửa.
3. Bản sửa được phát hành dưới dạng bản patch theo quy trình release thông thường.
4. Mọi phiên bản bị ảnh hưởng của cả 7 gói npm được đánh dấu bằng `npm deprecate`, trỏ tới phiên bản
   đã sửa, và các GitHub Release bị ảnh hưởng có thêm cảnh báo ở đầu phần ghi chú.
5. Chi tiết được công bố sau khi người dùng đã có thời gian nâng cấp.

## Chính sách không chứa secret

`sepay` là mã nguồn mở và **không chứa secret nào**. CLI không bao giờ được nhúng API key, token,
khóa ký, endpoint nội bộ hay thông tin xác thực của host.

- CLI chỉ xác thực bằng **OAuth2 token** mà người dùng lấy qua `sepay login`; CLI không bao giờ
  mang theo API key tĩnh của SePay.
- Token được lưu trong **keychain của hệ điều hành**, hoặc trong một file `0600` khi người dùng chọn
  `--insecure-storage`, hoặc khi `sepay login` không tìm thấy keychain dùng được (CLI báo điều này
  bằng một cảnh báo và ghi lựa chọn đó vào profile). Keychain lỗi sau khi đã đăng nhập là một lỗi,
  không bao giờ âm thầm chuyển sang file.
  Token **không bao giờ được in ra** (`login`, `auth status`, `--verbose` và các thông báo lỗi đều
  che token). Chỉ `sepay auth token` in token ra, và chỉ khi được gọi rõ ràng.
- Phiên đăng nhập chỉ đến từ `sepay login`; không có biến môi trường nào đưa token vào.
- CLI chỉ gọi SePay API **công khai**, không gọi endpoint riêng của CLI hay endpoint nội bộ. Host
  duy nhất khác là `registry.npmjs.org`, được hỏi tối đa một lần mỗi ngày để lấy phiên bản mới nhất;
  host này không nhận thông tin xác thực nào (`SEPAY_NO_UPDATE_NOTIFIER=1` tắt việc này).

Nếu bạn phát hiện secret bị viết cứng trong repository hoặc trong một binary đã phát hành, hãy coi đó
là lỗ hổng và báo cáo như trên.

## Chuỗi cung ứng

- Mọi commit đều được quét bằng gitleaks trong CI; thông tin xác thực giả trong test được đánh dấu
  `// gitleaks:allow` ở cuối chính dòng chứa giá trị đó (gitleaks không nhận chú thích đặt ở dòng riêng).
- Binary phát hành chỉ được build bởi GitHub Actions (`.github/workflows/release.yml`), mọi action
  đều được ghim bằng commit SHA đầy đủ.
- `checksums.txt` được ký keyless bằng cosign (GitHub OIDC); job npm từ chối đóng gói archive có
  checksum không nằm trong file đã ký.
- Archive phát hành khi repository đang public có build provenance attestation của GitHub, từ 0.1.3
  trở đi (0.1.0–0.1.2 không có). Kiểm tra bằng
  `gh attestation verify sepay_<version>_linux_amd64.tar.gz -R sepayvn/sepay-cli`. Lệnh kiểm tra
  chữ ký cosign có ở mục "Xác minh bản tải về" trong [README](README.md).
- Các gói npm được phát hành bởi chính workflow đó qua npm trusted publishing; không có npm token nào
  được lưu trong repository.

---

# Security policy

## Reporting a vulnerability

Do not open a public issue for anything security sensitive.

- **Primary channel:** [GitHub Private Vulnerability Reporting](https://github.com/sepayvn/sepay-cli/security/advisories/new).
- **Fallback:** email **support@sepay.vn**.

Include steps to reproduce and the affected version (`sepay version`).

## Scope

| In scope | Out of scope |
|---|---|
| The `sepay` binary (every install channel) | `api.sepay.vn` and the other SePay APIs |
| The install scripts `scripts/install.sh`, `scripts/install.ps1` | `my.sepay.vn` and the other SePay web apps |
| The npm packages (`sepay-cli`, `@sepayjsc/cli-*`) | |
| The update notifier | |

## Response times

| Step | Target |
|---|---|
| Acknowledge receipt | 3 business days |
| Assess severity and scope | 10 days |
| Coordinated disclosure | 90 days from the report |

## Supported versions

Before 1.0, only the latest release receives fixes.

| Version | Supported |
|---|---|
| latest 0.x | yes |
| older 0.x | no, upgrade |

## Handling a vulnerable version

1. Find the affected range (`git tag --contains <commit that introduced it>`).
2. Severe issues: new installs move to a safe version at once (npm dist-tag `latest`) while the fix
   is prepared.
3. The fix ships as a patch release through the normal release process.
4. Every affected version of all 7 npm packages is marked with `npm deprecate`, pointing to the fixed
   version, and the affected GitHub Releases get a warning at the top of their notes.
5. Details are published once users have had time to upgrade.

## No-secret policy

`sepay` is open source and **ships no secret**. The CLI never embeds an API key, token, signing key,
internal endpoint or host credential.

- The CLI authenticates only with the **OAuth2 token** a user obtains through `sepay login`; it never
  carries a static SePay API key.
- The token is stored in the **OS keychain**, or in a `0600` file when the user picks
  `--insecure-storage` or when `sepay login` finds no usable keychain (the CLI says so with a warning
  and records the choice in the profile). A keychain failure after login is an error; it never falls
  back to a file silently.
  The token is **never printed** (`login`, `auth status`, `--verbose` and error messages all redact
  it). Only `sepay auth token` prints it, and only when called explicitly.
- A session comes only from `sepay login`; no environment variable injects a token.
- Requests to SePay hosts carry a `User-Agent` with the CLI version, OS, CPU architecture and, when
  an AI agent launched the CLI, the agent's name (`sepay help environment`). The server only logs
  it; it is never used to authorize a request.
- The CLI calls only the **public** SePay API, never a CLI-private or internal endpoint. The one
  other host is `registry.npmjs.org`, asked at most once a day for the latest version; it receives no
  credential (`SEPAY_NO_UPDATE_NOTIFIER=1` turns this off).

If you find a secret hard-coded in the repository or in a released binary, treat it as a
vulnerability and report it as above.

## Supply chain

- Every commit is scanned with gitleaks in CI; fake credentials in tests carry
  `// gitleaks:allow` at the end of the line holding the value (gitleaks ignores it on a line of its own).
- Release binaries are built only by GitHub Actions (`.github/workflows/release.yml`), with every
  action pinned to a full commit SHA.
- `checksums.txt` is signed keyless with cosign (GitHub OIDC); the npm job refuses to package an
  archive whose checksum is not in the signed file.
- Release archives published while the repository is public carry a GitHub build provenance
  attestation, from 0.1.3 on (0.1.0–0.1.2 have none). Check it with
  `gh attestation verify sepay_<version>_linux_amd64.tar.gz -R sepayvn/sepay-cli`. The cosign check
  is under "Verify a download" in the [README](README.en.md).
- The npm packages are published by the same workflow through npm trusted publishing; no npm token is
  stored in the repository.
