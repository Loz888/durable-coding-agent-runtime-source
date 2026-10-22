import unittest
from clock_probe import ClockSeries, connection_environment


class ClockSeriesTest(unittest.TestCase):
    def test_monotonic_gap_is_not_a_clock_step(self):
        series = ClockSeries(100_000_000)
        series.add(9_000_000_000, 1_000_000_000)
        series.add(14_000_000_000, 6_000_000_000)
        self.assertEqual(series.result()["backward_steps"], 0)
        self.assertEqual(series.result()["forward_steps"], 0)
        self.assertEqual(series.result()["max_sample_gap_ms"], 5000)

    def test_backward_and_forward_adjustments_retain_raw_values(self):
        series = ClockSeries(100_000_000)
        series.add(9_000_000_000, 1_000_000_000)
        series.add(6_100_000_000, 1_010_000_000)
        series.add(10_100_000_000, 1_020_000_000)
        result = series.result()
        self.assertEqual(result["backward_steps"], 1)
        self.assertEqual(result["forward_steps"], 1)
        self.assertEqual(result["events"][0]["previous_wall_ns"], 9_000_000_000)
        self.assertEqual(result["events"][0]["adjustment_ms"], -2910)

    def test_preemption_between_clock_reads_is_not_a_clock_step(self):
        series = ClockSeries(100_000_000)
        series.add(1_000_000_000, 1_000_000_000)
        # Read wall at t=2, resume after a 250ms pause: midpoint +/-125ms.
        series.add(2_000_000_000, 2_125_000_000, 125_000_000)
        series.add(3_000_000_000, 3_000_000_000)
        self.assertEqual(series.result()["backward_steps"], 0)
        self.assertEqual(series.result()["forward_steps"], 0)

    def test_database_uncertainty_prevents_network_jitter_verdict(self):
        series = ClockSeries(100_000_000)
        series.add(1_000_000_000, 1_000_000_000, 80_000_000)
        series.add(2_250_000_000, 2_000_000_000, 80_000_000)
        self.assertEqual(series.result()["forward_steps"], 0)
        series.add(4_000_000_000, 3_000_000_000, 80_000_000)
        self.assertEqual(series.result()["forward_steps"], 1)


class ConnectionEnvironmentTest(unittest.TestCase):
    def test_keyword_dsn_preserves_quoted_escaped_values(self):
        env = connection_environment(r"host=/tmp/pg port=18569 dbname = example_test user=reviewer password='has space\'and\\slash'")
        self.assertEqual(env["PGDATABASE"], "example_test")
        self.assertEqual(env["PGPASSWORD"], "has space'and\\slash")
        self.assertEqual(env["PGHOST"], "/tmp/pg")

    def test_uri_decoding(self):
        env = connection_environment("postgresql://review%65r:TEST_PASSWORD@127.0.0.1:18569/example%5Ftest?sslmode=disable")
        self.assertEqual(env["PGPASSWORD"], "TEST_PASSWORD")
        self.assertEqual(env["PGUSER"], "reviewer")
        self.assertEqual(env["PGDATABASE"], "example_test")
        self.assertEqual(env["PGPORT"], "18569")

    def test_malformed_and_unsupported_configuration_refused(self):
        for dsn in ("password='unfinished", "host=x\\", "not-an-option", "unknown=secret", "host='x'junk"):
            with self.subTest(case=dsn):
                with self.assertRaises(ValueError):
                    connection_environment(dsn)


if __name__ == "__main__":
    unittest.main()
