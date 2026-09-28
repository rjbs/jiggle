use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Library;
use Jiggle::TestLibrary;

# Load the library afresh, and say how many metadata files had to be parsed.
sub reparsed_on_load ($root, $code = sub {}) {
  $code->();
  my $library = Jiggle::Library->new({ root => $root });
  return ($library->reparsed, $library);
}

subtest 'parsed metadata is cached' => sub {
  my (undef, $root) = library_with(photos => [
    { id => 'aaaa0001', title => 'one' },
    { id => 'aaaa0002', title => 'two' },
    { id => 'aaaa0003', title => 'three' },
  ]);

  my ($first) = reparsed_on_load($root);
  is($first, 3, 'first load parses everything');

  my ($second, $library) = reparsed_on_load($root);
  is($second, 0, 'second load parses nothing');
  is(scalar(() = $library->photos), 3, '...and still has every photo');

  my $meta = $library->meta_path('aaaa0002');
  my ($third, $edited) = reparsed_on_load($root, sub {
    $meta->spew_utf8($meta->slurp_utf8 =~ s/^title = "two"/title = "TWO"/mr);
  });
  is($third, 1, 'an edited file is parsed again');
  is($edited->photo('aaaa0002')->title, 'TWO', '...and the edit is seen');

  my ($fourth, $smaller) = reparsed_on_load($root, sub { $library->meta_path('aaaa0003')->remove });
  is(scalar(() = $smaller->photos), 2, 'a removed file is gone');
};

sub format_ok ($desc, $config, $want) {
  # The config goes in after setup, which itself loads the library.
  my (undef, $root) = library_with(photos => []);
  $root->child('jiggle.toml')->spew_utf8($config);

  my $library = eval { Jiggle::Library->new({ root => $root }) };

  if (defined $want) {
    ok($library, "$desc: loads") or return diag $@;
    is($library->format, $want, "$desc: format $want");
  } else {
    ok(! $library, "$desc: refused");
    like($@, qr/only knows format/, "$desc: ...saying why");
  }
}

format_ok('no format given', '', 1);
format_ok('format 1', "format = 1\n", 1);
format_ok('a newer format', "format = 2\n", undef);

done_testing;
