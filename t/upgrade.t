use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use Jiggle::Library;
use Jiggle::TestLibrary;
use Jiggle::Upgrade;

$ENV{GIT_AUTHOR_NAME}  = $ENV{GIT_COMMITTER_NAME}  = 'Test';
$ENV{GIT_AUTHOR_EMAIL} = $ENV{GIT_COMMITTER_EMAIL} = 'test@example.com';

sub git ($library, @args) {
  my $meta = $library->meta_dir;
  my $out = `git -C '$meta' @args 2>&1`;
  die "git @args: $out" if $?;
  return $out;
}

# A format-1 library: its jiggle.toml is $config, and each photo's metadata
# file gets the extra lines given for it (like "pending = true"), all
# committed.
sub format_1_library ($config, $extra) {
  my ($made) = library_with(photos => [
    map {; { id => $_, ($extra->{$_}{visibility} ? (visibility => $extra->{$_}{visibility}) : ()) } }
    sort keys %$extra
  ]);
  my $library = Jiggle::Library->new({ root => $made->root, allow_old_format => 1 });

  $library->root->child('jiggle.toml')->spew_utf8($config);
  for my $id (keys %$extra) {
    my $file = $library->meta_path($id);
    my $lines = join '', map {; "$_\n" } ($extra->{$id}{lines} // [])->@*;

    # After the visibility, so they're top-level keys, before any table.
    $file->spew_utf8($file->slurp_utf8 =~ s/^(visibility = .*\n)/$1$lines/mr);
  }

  git($library, 'init --quiet');
  git($library, 'add .');
  git($library, 'commit --quiet -m first');
  return Jiggle::Library->new({ root => $library->root, allow_old_format => 1 });
}

# Upgrades a format-1 library, and checks each photo's visibility afterward,
# and that only files that needed changing were changed.
sub upgrade_ok ($desc, $config, $extra, $want) {
  subtest "upgrade: $desc" => sub {
    my $library = format_1_library($config, $extra);
    my %before = map {; $_ => $library->meta_path($_)->slurp_utf8 } keys %$extra;

    my @done = Jiggle::Upgrade->new({ library => $library })->upgrade;
    is(scalar @done, 1, 'one step taken');
    like($done[0], $want->{says}, '...saying what it did') if $want->{says};

    my $after = eval { Jiggle::Library->new({ root => $library->root }) };
    ok($after, 'the library loads without allow_old_format') or return diag $@;
    is($after->format, 2, '...as format 2');
    like($after->root->child('jiggle.toml')->slurp_utf8, $want->{config}, 'jiggle.toml') if $want->{config};

    for my $id (sort keys $want->{visibility}->%*) {
      is($after->photo($id)->visibility, $want->{visibility}{$id}, "$id: visibility");
      unlike($after->meta_path($id)->slurp_utf8, qr/^pending/m, "$id: no pending key");
    }
    for my $id (($want->{untouched} // [])->@*) {
      is($after->meta_path($id)->slurp_utf8, $before{$id}, "$id: untouched");
    }

    is(git($after, 'status --porcelain'), '', 'everything committed');
    is_deeply([ Jiggle::Upgrade->new({ library => Jiggle::Library->new({ root => $after->root }) })->upgrade ],
      [], 'upgrading again does nothing');
  };
}

upgrade_ok('pending photos become pending, whatever their visibility',
  "format = 1\n# a comment, kept\ntitle = \"Photos\"\n",
  {
    aaaa0001 => { visibility => 'public',  lines => [ 'pending = true' ] },
    bbbb0002 => { visibility => 'private', lines => [ 'pending = true' ] },
    cccc0003 => { visibility => 'private', lines => [ 'pending = false' ] },
    dddd0004 => { visibility => 'public' },
  },
  {
    visibility => { aaaa0001 => 'pending', bbbb0002 => 'pending', cccc0003 => 'private', dddd0004 => 'public' },
    untouched  => [ 'dddd0004' ],
    config     => qr/\Aformat = 2\n# a comment, kept\ntitle = "Photos"\n\z/,
    says       => qr/\Aformat 1 to 2: 2 photo\(s\) were pending/,
  },
);

upgrade_ok('a library with no format, and nothing pending', "title = \"Photos\"\n",
  { aaaa0001 => { visibility => 'public' } },
  {
    visibility => { aaaa0001 => 'public' },
    untouched  => [ 'aaaa0001' ],
    config     => qr/\Aformat = 2\ntitle = "Photos"\n\z/,
    says       => qr/no photo had a pending key/,
  },
);

subtest 'what the upgrade commits' => sub {
  my $library = format_1_library("format = 1\n", { aaaa0001 => { lines => [ 'pending = true' ] } });
  Jiggle::Upgrade->new({ library => $library })->upgrade;
  is(git($library, 'log -1 --format=%B'),
    "upgrade the library to format 2: pending is a visibility\n\n1 photo(s) were pending.\n\n", 'the message');
  is(git($library, 'show --name-only --format= HEAD'), "aa/aaaa0001.toml\n", '...and only the changed file');
};

subtest 'a pending key is refused, not ignored' => sub {
  my $ok = eval { Jiggle::Photo->new({ id => 'x', original => {}, pending => 1 }); 1 };
  ok(! $ok, 'refused');
  like($@, qr/needs jiggle upgrade/, '...saying what to do');
};

done_testing;
