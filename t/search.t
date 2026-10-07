use v5.36;

use Test::More;

use lib 'lib', 't/lib';

use IO::Uncompress::Gunzip ();
use JSON::MaybeXS ();
use Jiggle::Search;
use Jiggle::Site;
use Jiggle::TestLibrary;

my $search = Jiggle::Search->new;

plan skip_all => 'Pagefind is needed (it runs via npx, which may need the network once)'
  unless system(join(' ', $search->command->@*, '--version', '>/dev/null 2>&1')) == 0;

# Every search fragment in a built site, as { url => content }.  A fragment is
# gzipped JSON after a short signature.
sub indexed ($dir) {
  my %indexed;
  for my $file ($dir->child('pagefind/fragment')->children) {
    IO::Uncompress::Gunzip::gunzip("$file", \my $raw)
      or die "can't gunzip $file: $IO::Uncompress::Gunzip::GunzipError";
    $raw =~ s/\Apagefind_dcd//;
    my $fragment = JSON::MaybeXS::decode_json($raw);
    $indexed{ $fragment->{url} } = $fragment->{content};
  }
  return \%indexed;
}

sub indexed_urls_are ($desc, $dir, $want) {
  is_deeply([ sort keys indexed($dir)->%* ], [ sort @$want ], $desc);
}

sub rebuild ($library) {
  my $site = Jiggle::Site->new({ library => $library, search => $search });
  $site->build;
  return $site;
}

subtest 'only public photo pages are indexed' => sub {
  my ($site, $dir) = built_site(
    photos => [
      { id => 'pub00001', title => 'Stephansdom', tags => [ 'church' ] },
      { id => 'priv0001', title => 'Secret', visibility => 'private' },
    ],
    albums => [ { slug => 'vienna', title => 'Vienna', photos => [ 'pub00001' ] } ],
    site => { search => $search },
  );

  indexed_urls_are('photo pages, and nothing else (not their pages in albums)', $dir, [ '/p/pub00001/' ]);
  like(indexed($dir)->{'/p/pub00001/'}, qr/Stephansdom.*church/, 'title and tags indexed');
  unlike(indexed($dir)->{'/p/pub00001/'}, qr/\bTags\b/, 'labels are not indexed');
};

subtest 'a photo made private leaves the index' => sub {
  my ($site, $dir, $library) = built_site(
    photos => [ { id => 'aaaa0001' }, { id => 'bbbb0001' } ],
    site   => { search => $search },
  );
  indexed_urls_are('both indexed at first', $dir, [ '/p/aaaa0001/', '/p/bbbb0001/' ]);

  # Change the photo's metadata on disk, as a person would, then rebuild with
  # a fresh library object.  The stale page is still in site/ when the build
  # starts, which is exactly the case that matters.
  my $meta = $library->meta_path('bbbb0001');
  $meta->spew_utf8($meta->slurp_utf8 =~ s/^visibility = "public"/visibility = "private"/mr);

  rebuild(Jiggle::Library->new({ root => $library->root }));
  indexed_urls_are('only the public one remains', $dir, [ '/p/aaaa0001/' ]);
};

subtest 'an unchanged site rewrites no index files' => sub {
  my ($site, $dir, $library) = built_site(
    photos => [ { id => 'cccc0001', title => 'Unchanging' } ],
    site   => { search => $search },
  );

  my $again = rebuild(Jiggle::Library->new({ root => $library->root }));
  is($again->writer->stats->{written}, 0, 'nothing written');
  is($again->writer->stats->{pruned},  0, 'nothing pruned');
};

# A Jiggle::Search that counts how often Pagefind is run.
package Counting::Search {
  use parent -norequire, 'Jiggle::Search';
  our $RUNS = 0;
  sub index_site { $RUNS++; shift->SUPER::index_site(@_) }
}

sub runs_while_building ($library, $code = sub {}) {
  $code->();
  local $Counting::Search::RUNS = 0;
  Jiggle::Site->new({
    library => Jiggle::Library->new({ root => $library->root }),
    search  => bless({ %$search }, 'Counting::Search'),
  })->build;
  return $Counting::Search::RUNS;
}

subtest 'Pagefind runs only when a page changed' => sub {
  my (undef, $dir, $library) = built_site(
    photos => [ { id => 'dddd0001', title => 'Before' } ],
    site   => { search => $search },
  );

  is(runs_while_building($library), 0, 'no page changed: not run');
  indexed_urls_are('...and the index is kept', $dir, [ '/p/dddd0001/' ]);

  my $meta = $library->meta_path('dddd0001');
  is(runs_while_building($library, sub {
    $meta->spew_utf8($meta->slurp_utf8 =~ s/^title = "Before"/title = "After"/mr);
  }), 1, 'a title changed: run');
  like(indexed($dir)->{'/p/dddd0001/'}, qr/After/, '...and the index has the new title');
};

subtest 'building without search' => sub {
  my (undef, $dir, $library) = built_site(
    photos => [ { id => 'eeee0001', title => 'Before' } ],
    site   => { search => $search },
  );

  my $build = sub {
    Jiggle::Site->new({ library => Jiggle::Library->new({ root => $library->root }) })->build;
  };

  $build->();
  indexed_urls_are('no page changed: the index is kept', $dir, [ '/p/eeee0001/' ]);

  my $meta = $library->meta_path('eeee0001');
  $meta->spew_utf8($meta->slurp_utf8 =~ s/^visibility = "public"/visibility = "private"/mr);
  $build->();
  ok(! -e $dir->child('pagefind'), 'pages changed: the stale index is removed, not kept');
};

done_testing;
