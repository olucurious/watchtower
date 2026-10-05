# Synthetic AppSignal Ruby harness: placeholder key, loopback receiver.
require "appsignal"

module Library
  class Catalog
    def self.fetch!(isbn) = { "978-0" => 1 }.fetch(isbn)
    def self.reserve!(copies) = copies > 3 ? raise(ArgumentError, "cannot reserve #{copies} copies; the limit is 3") : true
    def self.deep(n) = n.zero? ? raise("deep failure") : deep(n - 1) + 1
  end
end

Appsignal.configure(:production) do |config|
  config.active = true
  config.name = "library"
  config.push_api_key = ENV.fetch("APPSIGNAL_PUSH_API_KEY", "00000000-0000-0000-0000-000000000000")
  config.revision = "library@synthetic-ruby"
  config.hostname = "matrix-host"
  config.enable_host_metrics = false
  config.enable_minutely_probes = false
end
Appsignal.start

def step(name)
  yield
  puts "STEP #{name} ok"
rescue => e
  puts "STEP #{name} raised #{e.class}: #{e.message}"
end

step("send_error_with_metadata") do
  begin
    Library::Catalog.fetch!("missing")
  rescue => e
    Appsignal.send_error(e) do
      Appsignal.set_namespace("background")
      Appsignal.add_tags(region: "eu", request_id: "req-123")
      Appsignal.add_params(password: "hunter22", api_token: "tok_live_123")
    end
  end
end

step("monitor_error") do
  Appsignal.monitor(namespace: "background_job", action: "library.reserve") do
    Library::Catalog.reserve!(5)
  rescue => e
    Appsignal.set_error(e)
  end
end

step("report_error") do
  begin
    Library::Catalog.fetch!("also-missing")
  rescue => e
    Appsignal.report_error(e)
  end
end

class CatalogError < StandardError; end

step("error_with_cause") do
  begin
    begin
      Library::Catalog.fetch!("missing-cause")
    rescue KeyError
      raise CatalogError, "book could not be reserved"
    end
  rescue => e
    Appsignal.send_error(e)
  end
end

step("burst_25_errors") do
  25.times do |n|
    begin
      Library::Catalog.deep(n % 5)
    rescue => e
      Appsignal.send_error(RuntimeError.new("deep failure #{n + 1}").tap { |x| x.set_backtrace(e.backtrace) })
    end
  end
end

sleep 3
Appsignal.stop("harness")
sleep 3
puts "HARNESS_DONE ruby #{Appsignal::VERSION}"
