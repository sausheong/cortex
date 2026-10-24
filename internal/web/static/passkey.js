// Passkey enrollment and unlock. Runs the WebAuthn ceremonies and the PRF
// extension in the browser. The PRF output goes to the server over TLS in the
// request body and is not kept here. No inline handlers: this file is loaded
// with the page's nonce and wires its own listeners.
(function () {
  "use strict";
  var root = document.getElementById("passkey-root");
  if (!root) return;
  var csrf = root.dataset.csrf;
  var mode = root.dataset.mode;
  var errBox = document.getElementById("pk-error");

  function b64uToBuf(s) {
    s = s.replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var bin = atob(s), a = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) a[i] = bin.charCodeAt(i);
    return a.buffer;
  }
  function bufToB64u(buf) {
    var a = new Uint8Array(buf), s = "";
    for (var i = 0; i < a.length; i++) s += String.fromCharCode(a[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function showError(msg) {
    if (!errBox) return;
    errBox.textContent = msg;
    errBox.hidden = false;
  }
  function clearError() {
    if (errBox) { errBox.hidden = true; errBox.textContent = ""; }
  }
  function status(id, msg) {
    var el = document.getElementById(id);
    if (el) el.textContent = msg || "";
  }

  function post(path, body) {
    return fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(body || {})
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (j) {
        if (!r.ok) throw new Error(j.error || "Something went wrong. Try again.");
        return j;
      });
    });
  }

  // The server sends WebAuthn JSON (base64url). Browsers want buffers.
  function prfInput(ext) {
    if (!ext || !ext.prf) return undefined;
    var p = ext.prf, out = {};
    if (p.eval && p.eval.first) out.eval = { first: b64uToBuf(p.eval.first) };
    if (p.evalByCredential) {
      out.evalByCredential = {};
      Object.keys(p.evalByCredential).forEach(function (k) {
        out.evalByCredential[k] = { first: b64uToBuf(p.evalByCredential[k].first) };
      });
    }
    return { prf: out };
  }
  function descriptor(d) {
    return { type: d.type, id: b64uToBuf(d.id), transports: d.transports };
  }

  function prfFrom(cred) {
    var ext = cred.getClientExtensionResults ? cred.getClientExtensionResults() : {};
    if (ext && ext.prf && ext.prf.results && ext.prf.results.first) {
      return bufToB64u(ext.prf.results.first);
    }
    return null;
  }

  function create(options) {
    var pk = options.publicKey;
    return navigator.credentials.create({
      publicKey: {
        challenge: b64uToBuf(pk.challenge),
        rp: pk.rp,
        user: { id: b64uToBuf(pk.user.id), name: pk.user.name, displayName: pk.user.displayName },
        pubKeyCredParams: pk.pubKeyCredParams,
        timeout: pk.timeout,
        excludeCredentials: (pk.excludeCredentials || []).map(descriptor),
        authenticatorSelection: pk.authenticatorSelection,
        attestation: pk.attestation,
        extensions: prfInput(pk.extensions)
      }
    }).then(function (cred) {
      var r = cred.response;
      return {
        prf: prfFrom(cred),
        credId: cred.rawId,
        attestation: {
          id: cred.id,
          rawId: bufToB64u(cred.rawId),
          type: cred.type,
          response: {
            clientDataJSON: bufToB64u(r.clientDataJSON),
            attestationObject: bufToB64u(r.attestationObject),
            transports: r.getTransports ? r.getTransports() : []
          },
          clientExtensionResults: {}
        }
      };
    });
  }

  // get runs an assertion. extensions, when given, replaces the options'
  // extension inputs (used right after creation).
  function get(options, extensions) {
    var pk = options.publicKey;
    return navigator.credentials.get({
      publicKey: {
        challenge: b64uToBuf(pk.challenge),
        rpId: pk.rpId,
        timeout: pk.timeout,
        userVerification: pk.userVerification,
        allowCredentials: (pk.allowCredentials || []).map(descriptor),
        extensions: prfInput(extensions || pk.extensions)
      }
    }).then(function (cred) {
      var r = cred.response;
      var resp = {
        clientDataJSON: bufToB64u(r.clientDataJSON),
        authenticatorData: bufToB64u(r.authenticatorData),
        signature: bufToB64u(r.signature)
      };
      if (r.userHandle) resp.userHandle = bufToB64u(r.userHandle);
      return {
        prf: prfFrom(cred),
        assertion: { id: cred.id, rawId: bufToB64u(cred.rawId), type: cred.type, response: resp, clientExtensionResults: {} }
      };
    });
  }

  function guard(fn, btn) {
    return function () {
      clearError();
      if (btn) btn.disabled = true;
      Promise.resolve().then(fn).catch(function (e) {
        var msg = e && e.name === "NotAllowedError"
          ? "The passkey prompt was cancelled or timed out. Try again."
          : (e && e.message) || "Something went wrong. Try again.";
        showError(msg);
      }).then(function () { if (btn) btn.disabled = false; });
    };
  }

  var NO_PRF = "This device cannot protect your memory with a passkey. Use a passkey that supports PRF, or another device.";

  if (mode === "unlock") {
    var ub = document.getElementById("unlock-btn");
    if (ub) {
      ub.addEventListener("click", guard(function () {
        status("pk-status", "Waiting for your passkey.");
        return post("/unlock/begin").then(function (b) {
          return get(b.options).then(function (g) {
            if (!g.prf) throw new Error("This passkey did not return a key. Use your recovery key instead.");
            return post("/unlock/finish", {
              ceremony: b.ceremony, assertion: g.assertion, prf_output: g.prf, next: root.dataset.next || "/"
            });
          });
        }).then(function (j) {
          window.location.assign(j.next || "/");
        }).catch(function (e) { status("pk-status", ""); throw e; });
      }, ub));
    }
    return;
  }

  // Enrollment.
  var state = {};
  var startBtn = document.getElementById("start-btn");
  var confirmBtn = document.getElementById("confirm-btn");

  if (startBtn) {
    startBtn.addEventListener("click", guard(function () {
      if (!document.getElementById("confirm-loss").checked) {
        throw new Error("Tick the box to confirm you understand that lost keys cannot be recovered.");
      }
      if (!window.PublicKeyCredential) throw new Error(NO_PRF);
      status("pk-status", "Waiting for your passkey.");
      return post("/enroll/begin").then(function (b) {
        return create(b.options).then(function (c) {
          if (c.prf) return { b: b, c: c, prf: c.prf };
          // Some authenticators return PRF output only on a later assertion.
          var salt = b.options.publicKey.extensions && b.options.publicKey.extensions.prf &&
            b.options.publicKey.extensions.prf.eval;
          if (!salt) throw new Error(NO_PRF);
          var idb = bufToB64u(c.credId);
          var ext = { prf: { evalByCredential: {} } };
          ext.prf.evalByCredential[idb] = { first: salt.first };
          // This assertion only yields the PRF output; the server verifies
          // the later proof round, not this one.
          var req = { publicKey: {
            challenge: b.options.publicKey.challenge,
            rpId: b.options.publicKey.rp.id,
            timeout: b.options.publicKey.timeout,
            userVerification: "required",
            allowCredentials: [{ type: "public-key", id: idb }]
          } };
          return get(req, ext).then(function (g) {
            if (!g.prf) throw new Error(NO_PRF);
            return { b: b, c: c, prf: g.prf };
          });
        });
      }).then(function (x) {
        return post("/enroll/finish", {
          ceremony: x.b.ceremony, attestation: x.c.attestation, prf_output: x.prf,
          name: document.getElementById("passkey-name").value, confirm_loss: true
        });
      }).then(function (j) {
        state.confirm = j.confirm;
        document.getElementById("recovery-key").textContent = j.recovery;
        document.getElementById("group-a-label").textContent = "Group " + j.positions[0];
        document.getElementById("group-b-label").textContent = "Group " + j.positions[1];
        document.getElementById("step-passkey").hidden = true;
        document.getElementById("step-recovery").hidden = false;
        document.getElementById("recovery-key").focus();
      }).catch(function (e) { status("pk-status", ""); throw e; });
    }, startBtn));
  }

  if (confirmBtn) {
    confirmBtn.addEventListener("click", guard(function () {
      var groups = [document.getElementById("group-a").value, document.getElementById("group-b").value];
      if (!groups[0].trim() || !groups[1].trim()) throw new Error("Type both groups of your recovery key.");
      status("pk-status-2", "Waiting for your passkey.");
      return post("/enroll/confirm/begin", { confirm: state.confirm }).then(function (b) {
        return get(b.options).then(function (g) {
          if (!g.prf) throw new Error(NO_PRF);
          return post("/enroll/confirm", { confirm: state.confirm, assertion: g.assertion, prf_output: g.prf, groups: groups });
        });
      }).then(function (j) {
        window.location.assign(j.next || "/unlock");
      }).catch(function (e) { status("pk-status-2", ""); throw e; });
    }, confirmBtn));
  }
})();
