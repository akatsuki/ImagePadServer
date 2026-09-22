import copy
import unittest

from make_t11_fixture import build_high_density_snapshot


class T11FixtureTests(unittest.TestCase):
    def test_high_density_fixture_is_deterministic_and_does_not_mutate_source(self):
        source = {"videoId": "sm9", "threads": [{"id": "t", "comments": [{"id": "1", "no": 7, "vposMs": 999, "body": "x"}]}]}
        original = copy.deepcopy(source)
        result = build_high_density_snapshot(source, duration_ms=1000, multiplier=3)
        self.assertEqual(source, original)
        comments = result["threads"][0]["comments"]
        self.assertEqual(len(comments), 3)
        self.assertEqual(len({comment["id"] for comment in comments}), 3)
        self.assertTrue(all(0 <= comment["vposMs"] < 1000 for comment in comments))
        self.assertEqual(result["threads"][0]["commentCount"], 3)


if __name__ == "__main__":
    unittest.main()
