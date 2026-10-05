# Synthetic AppSignal capture harness. Placeholder key, loopback receiver,
# no real application code or credentials. Usage: APPSIGNAL_VSN=2.17.4 elixir harness.exs
vsn = System.fetch_env!("APPSIGNAL_VSN")
# Optional dependency pins, e.g. from a lockfile that uses this version,
# e.g. EXTRA_DEPS="finch:0.19.0,hackney:1.23.0".
extra =
  for pin <- String.split(System.get_env("EXTRA_DEPS", ""), ",", trim: true) do
    [name, v] = String.split(pin, ":")
    {String.to_atom(name), v, override: true}
  end
Mix.install(
  [{:appsignal, vsn, override: true}, {:appsignal_plug, "~> 2.0"}, {:plug, "~> 1.14"}, {:jason, "~> 1.4"}] ++ extra,
  config: [appsignal: [config: [otp_app: :library, name: "library", env: :prod, active: true,
    revision: "library@synthetic-#{vsn}", hostname: "matrix-host",
    push_api_key: "00000000-0000-0000-0000-000000000000"]]])

defmodule Library.Catalog do
  def fetch!(isbn), do: Map.fetch!(%{"978-0" => 1}, isbn)
  def reserve!(copies) when copies > 3, do: raise(ArgumentError, "cannot reserve #{copies} copies; the limit is 3")
  def deep(0), do: raise(RuntimeError, "deep failure")
  def deep(n), do: deep(n - 1) + 1
end

defmodule Library.Router do
  use Plug.Router
  use Appsignal.Plug
  plug :match
  plug :dispatch
  post "/books/:id/reserve" do
    _ = conn.params
    Library.Catalog.fetch!("missing-" <> id)
  end
end

defmodule Library.Indexer do
  use GenServer
  def init(_), do: {:ok, %{}}
  def handle_cast(:crash, _), do: raise(KeyError, key: :shelf, term: %{})
end

defmodule Run do
  def step(name, fun) do
    try do
      fun.()
      IO.puts("STEP #{name} ok")
    rescue
      e -> IO.puts("STEP #{name} raised #{inspect(e.__struct__)}")
    catch
      kind, reason -> IO.puts("STEP #{name} #{kind} #{inspect(reason)}")
    end
  end
end

Run.step("send_error_with_metadata", fn ->
  try do
    Library.Catalog.fetch!("missing")
  rescue
    e ->
      Appsignal.send_error(e, __STACKTRACE__, fn span ->
        span
        |> Appsignal.Span.set_namespace("background")
        |> Appsignal.Span.set_sample_data("tags", %{"region" => "eu", "request_id" => "req-123"})
        |> Appsignal.Span.set_sample_data("params", %{"password" => "hunter22", "api_token" => "tok_live_123"})
      end)
  end
end)

Run.step("instrumented_span_error", fn ->
  Appsignal.instrument("library.reserve", fn ->
    try do
      Library.Catalog.reserve!(5)
    rescue
      e -> Appsignal.set_error(e, __STACKTRACE__)
    end
  end)
end)

Run.step("plug_request_crash", fn ->
  conn = Plug.Test.conn(:post, "/books/42/reserve?token=secret-token", %{"copies" => "1"})
  Library.Router.call(conn, Library.Router.init([]))
end)

Run.step("genserver_crash", fn ->
  Process.flag(:trap_exit, true)
  {:ok, pid} = GenServer.start_link(Library.Indexer, nil)
  GenServer.cast(pid, :crash)
  receive do {:EXIT, ^pid, _} -> :ok after 2_000 -> :timeout end
end)

Run.step("exit_and_throw", fn ->
  Appsignal.send_error(:exit, :index_timeout, [])
  try do throw(:shelf_full) catch kind, reason -> Appsignal.send_error(kind, reason, __STACKTRACE__) end
end)

Run.step("burst_25_errors", fn ->
  for n <- 1..25 do
    try do
      Library.Catalog.deep(rem(n, 5))
    rescue
      e -> Appsignal.send_error(%{e | message: "deep failure #{n}"}, __STACKTRACE__)
    end
  end
end)

Process.sleep(5_000)
Appsignal.Nif.stop()
Process.sleep(3_000)
IO.puts("HARNESS_DONE #{vsn}")
