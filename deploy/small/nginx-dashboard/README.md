# nginx-dashboard — edge auth & operator credential

`nginx-dashboard` is the ingress router for the small profile. It terminates the
dashboard SPA, proxies the gateway API, and gates everything behind **HTTP Basic
Auth** (realm `UBAG Operator`). After Basic Auth passes, nginx injects the gateway
Bearer token into `/v1/*` server-side, so the gateway secret never reaches the
browser. See `default.conf.template` for the full routing/security model.

## The operator credential (important)

The Basic Auth user (`operator` by default) is stored **only** as a one-way hash
in `.htpasswd` (this directory). That file is **gitignored on purpose** — password
hashes are never committed — and it is bind-mounted read-only into the container at
`/etc/nginx/.htpasswd`.

There is **no plaintext operator password** in `env.local` or anywhere else, so the
password cannot be recovered — only re-set.

### Symptom: the login "keeps asking for the password"

If the browser keeps re-showing the native Sign-in dialog even with what you believe
is the correct password, the stored hash no longer matches what you're typing. Confirm
with the nginx log:

```
docker logs ubag-small-nginx-dashboard-1 2>&1 | grep 'password mismatch'
# -> user "operator": password mismatch
```

`password mismatch` (as opposed to `no user` / `could not open`) means the file is
fine and nginx is simply rejecting the password. The usual cause is a **secret
rotation** that regenerated `.htpasswd` — which silently locks out the operator,
because the old plaintext is gone.

### Fix: reset the operator password

```
cd deploy/small/nginx-dashboard
./set-operator-password.sh                 # generate a strong random password (printed once)
# or:
./set-operator-password.sh 'my-password'   # set a specific password
```

The script backs up the current file, writes a fresh LF-only htpasswd, and hot-reloads
nginx if the container is running. Cloudflare fronts the site but does **not** cache the
401 (`Cf-Cache-Status: DYNAMIC`, `Cache-Control: no-cache`), so the change takes effect
immediately end-to-end.

### Verify

```
# Against the origin (bypasses Cloudflare); expect 200 for the right password, 401 for a wrong one:
curl -s -o /dev/null -w '%{http_code}\n' -u operator:PASSWORD http://127.0.0.1:8083/dashboard/
```
