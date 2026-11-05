# Demo of the first-webhook flow, recorded with charmbracelet/vhs:
#
#   vhs docs/demo.tape
#
# Needs `sepay` on PATH, logged in (`sepay login`) to a sandbox profile, plus jq and python3.
# The run creates a bank account and a webhook in that sandbox; `sepay sandbox reset` clears them.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 720
Set Padding 20
Set TypingSpeed 40ms

# A stand-in handler on localhost:3000 that answers 200 to every POST.
Hide
Type `export SEPAY_NO_UPDATE_NOTIFIER=1 PS1='$ '`
Enter
Type `python3 -c "exec('import http.server as h\nclass H(h.BaseHTTPRequestHandler):\n def do_POST(s):\n  s.rfile.read(int(s.headers.get(\'Content-Length\', 0))); s.send_response(200); s.end_headers()\n def log_message(s, *a): pass\nh.HTTPServer((\'127.0.0.1\', 3000), H).serve_forever()')" &`
Enter
Sleep 1s
Type "clear"
Enter
Show

Type "# 1. Provision a sandbox bank account and a webhook for listen"
Enter
Type "BA=$(sepay sandbox init --with-webhook-listen --yes --json | jq -r .bank_account); echo $BA"
Enter
Sleep 4s

Type "# 2. Forward every delivery to the handler on localhost:3000"
Enter
Type "sepay webhook listen --forward localhost:3000 &"
Enter
Sleep 4s

Type "# 3. Fire a sandbox transfer and watch it arrive"
Enter
Type "sepay sandbox simulate payment_in --bank-account $BA --amount 50000 --code DEMO01"
Enter
Sleep 8s

Hide
Type "kill %2 %1"
Enter
Show
Sleep 1s
