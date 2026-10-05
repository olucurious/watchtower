# Synthetic AppSignal Python harness: placeholder key, loopback receiver.
import os
import time
from appsignal import Appsignal, send_error, set_error, set_namespace, set_params, set_root_name, set_tag
from opentelemetry import trace

appsignal = Appsignal(active=True, name="library", push_api_key=os.environ.get("APPSIGNAL_PUSH_API_KEY", "00000000-0000-0000-0000-000000000000"),
                      environment="production", revision="library@synthetic-python", hostname="matrix-host",
                      enable_host_metrics=False, enable_minutely_probes=False, log_path="/tmp")
appsignal.start()
tracer = trace.get_tracer(__name__)

class CatalogError(Exception): pass

def fetch(isbn):
    return {"978-0": 1}[isbn]

def reserve(copies):
    if copies > 3:
        raise ValueError(f"cannot reserve {copies} copies; the limit is 3")

def deep(n):
    if n == 0:
        raise RuntimeError("deep failure")
    return deep(n - 1) + 1

def step(name, fn):
    try:
        fn(); print(f"STEP {name} ok")
    except Exception as e:
        print(f"STEP {name} raised {e!r}")

def with_metadata():
    try:
        fetch("missing")
    except KeyError as e:
        with tracer.start_as_current_span("send-error-scope"):
            set_namespace("background")
            set_tag("region", "eu"); set_tag("request_id", "req-123")
            set_params({"password": "hunter22", "api_token": "tok_live_123"})
            set_error(e)

def span_error():
    with tracer.start_as_current_span("library.reserve"):
        set_root_name("library.reserve")
        set_namespace("background_job")
        try:
            reserve(5)
        except ValueError as e:
            set_error(e)

def with_cause():
    try:
        try:
            fetch("missing-cause")
        except KeyError as inner:
            raise CatalogError("book could not be reserved") from inner
    except CatalogError as e:
        send_error(e)

def burst():
    for n in range(1, 26):
        try:
            deep(n % 5)
        except RuntimeError as e:
            send_error(RuntimeError(f"deep failure {n}").with_traceback(e.__traceback__))

step("send_error_with_metadata", with_metadata)
step("span_error", span_error)
step("error_with_cause", with_cause)
step("burst_25_errors", burst)
time.sleep(4)
appsignal.stop()
time.sleep(3)
print("HARNESS_DONE python")
