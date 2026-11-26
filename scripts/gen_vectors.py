# Regenerates testdata/reference_vectors.json using botocore and pycognito.
# Usage: python3 -m venv .venv && .venv/bin/pip install botocore pycognito
#        .venv/bin/python scripts/gen_vectors.py > testdata/reference_vectors.json
# Note: botocore uses the real clock for X-Amz-Date; the vector records it.
import json, datetime, base64
from unittest import mock
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
from botocore.credentials import Credentials

out = {}
creds = Credentials("AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "SESSIONTOKENEXAMPLE")
fixed = datetime.datetime(2026, 11, 25, 12, 34, 56, tzinfo=datetime.timezone.utc)
cases = [
  ("GET", "https://api.pentair.cloud/device/device-service/user/devices", None, {"x-amz-id-token": "IDTOKEN"}),
  ("POST", "https://api.pentair.cloud/device2/device2-service/user/device", '{"deviceIds":["abc"]}', {"x-amz-id-token": "IDTOKEN", "content-type": "application/json; charset=UTF-8"}),
  ("PUT", "https://api.pentair.cloud/device/device-service/user/device/abc", '{"payload":{"d13":"1"}}', {"x-amz-id-token": "IDTOKEN", "content-type": "application/json; charset=UTF-8"}),
  ("GET", "https://api.pentair.cloud/a/b?z=1&a=2&a=1", None, {"x-amz-id-token": "IDTOKEN"}),
]
sig = []
with mock.patch("botocore.auth.datetime") as dt:
    dt.datetime.now.return_value = fixed
    dt.datetime.utcnow.return_value = fixed.replace(tzinfo=None)
    for m, u, b, h in cases:
        r = AWSRequest(method=m, url=u, data=b, headers=h)
        SigV4Auth(creds, "execute-api", "us-west-2").add_auth(r)
        sig.append({"method": m, "url": u, "body": b, "headers": h,
                    "authorization": r.headers["Authorization"], "amzdate": r.headers["X-Amz-Date"]})
out["sigv4"] = sig

# SRP
from pycognito import aws_srp
a_hex = "5" * 256
with mock.patch.object(aws_srp.AWSSRP, "generate_random_small_a", lambda self: int(a_hex, 16)):
    srp = aws_srp.AWSSRP(username="user@example.com", password="Secret123!", pool_id="us-west-2_lbiduhSwD",
                         client_id="3de110o697faq7avdchtf07h4v", client=mock.MagicMock())
A = srp.large_a_value
# pick a server b and verifier to produce a realistic B
N = int(aws_srp.N_HEX, 16); g = 2
salt_hex = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
user_id = "11111111-2222-3333-4444-555555555555"
import hashlib
up = hashlib.sha256(("lbiduhSwD" + user_id + ":" + "Secret123!").encode()).hexdigest()
x = int(aws_srp.hex_hash(aws_srp.pad_hex(salt_hex) + up), 16)
v = pow(g, x, N); b = int("7"*200, 16); k = srp.val_k
B = (k*v + pow(g, b, N)) % N
secret_block = base64.standard_b64encode(b"secret-block-bytes").decode()
ts = "Wed Nov 5 09:08:07 UTC 2026"
with mock.patch("pycognito.aws_srp.datetime") as dt:
    dt.datetime.utcnow.return_value = datetime.datetime(2026,11,5,9,8,7)
    dt.datetime.now.return_value = datetime.datetime(2026,11,5,9,8,7, tzinfo=datetime.timezone.utc)
    resp = srp.process_challenge({"USER_ID_FOR_SRP": user_id, "SALT": salt_hex, "SRP_B": format(B, "x"),
                                  "SECRET_BLOCK": secret_block, "USERNAME": user_id}, {"USERNAME": user_id})
out["srp"] = {"a": a_hex, "A": format(A, "x"), "salt": salt_hex, "B": format(B, "x"), "b": format(b,"x"),
              "user_id": user_id, "password": "Secret123!", "pool": "us-west-2_lbiduhSwD",
              "secret_block": secret_block, "response": resp}
print(json.dumps(out, indent=1))
