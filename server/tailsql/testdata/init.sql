-- Canned test data for tailsql.
DROP TABLE IF EXISTS users;
CREATE TABLE users (
  name TEXT UNIQUE NOT NULL,
  title TEXT,
  location TEXT
);

-- If these values change, you may need to update the tests.
INSERT INTO users VALUES ('alice','ceo','amsterdam');
INSERT INTO users VALUES ('carole','cto','california');
INSERT INTO users VALUES ('dave','head of people','california');
INSERT INTO users VALUES ('eve','head of product','california');
INSERT INTO users VALUES ('mallory','eng','arizona');
INSERT INTO users VALUES ('athena','eng','north dakota');
INSERT INTO users VALUES ('asha','eng','oregon');
INSERT INTO users VALUES ('trent','technical writer','california');
INSERT INTO users VALUES ('michael','eng','washington');
INSERT INTO users VALUES ('amelie','mascot',NULL);

DROP TABLE IF EXISTS misc;
CREATE TABLE misc (x);

-- These test cases exercise output decoration in the UI.

-- A Stripe customer ID (fake)
INSERT INTO misc VALUES ('cus_Fak3Cu6t0m3rId');
-- A Stripe invoice ID (fake)
INSERT INTO misc VALUES ('in_1f4k31nv0Ic3Num83r');
-- A Stripe subscription ID (fake)
INSERT INTO misc VALUES ('sub_fAk34sH3l1anDMn0tgNatKT');
-- JSON text.
INSERT INTO misc VALUES ('{"json":true}');
-- SQL schema definition.
INSERT INTO misc VALUES ('CREATE TABLE misc (x);');
-- Stable IDs.
INSERT INTO misc VALUES ('godoc:tailscale.com/tailcfg.User');
-- URL links.
INSERT INTO misc VALUES ('https://github.com?q=1&r=2');
