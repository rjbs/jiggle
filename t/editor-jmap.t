use v5.36;

use Test::More;

# The model of the editor that jiggle-go serves is tested in JavaScript, by
# node's own test runner, which prints TAP.  So this only finds node, and
# becomes it.  -- claude, 2026-11-23
my ($major) = (`node --version 2>/dev/null` // '') =~ /\Av([0-9]+)\./;
plan skip_all => 'needs node 20 or newer' unless $major && $major >= 20;

exec 'node', '--test', '--test-reporter=tap', 't/editor-jmap/' or die "can't run node: $!";
