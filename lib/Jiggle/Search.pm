package Jiggle::Search;
use v5.36;

use Moo;

use Path::Tiny ();

=head1 NAME

Jiggle::Search - build the site's search index with Pagefind

=head1 DESCRIPTION

L<Pagefind|https://pagefind.app/> reads the built HTML and produces a static
search index, split into chunks so that a search downloads only the parts it
needs.  Its output is deterministic, and its chunk files are named by their
content, so an unchanged site gives an unchanged index.

Pagefind writes into a scratch directory, and each file is then put into the
site through the L<Jiggle::Site::Writer>, so write-if-changed applies to the
index as it does to everything else.

Only what's in the site can be indexed, so private photos are never in the
index, provided stale pages were pruned before Pagefind ran.
L<Jiggle::Site> takes care of that.

=cut

# Pinned, so that a new Pagefind release can't change the index format (and
# so every index file) without our noticing.  -- claude, 2026-09-27
our $PAGEFIND_VERSION = '1.5.2';

has command => (
  is => 'lazy',
  default => sub {
    my $on_path = grep {; -x "$_/pagefind" } split /:/, $ENV{PATH};
    return $on_path ? [ 'pagefind' ] : [ qw( npx --yes ), "pagefind\@$PAGEFIND_VERSION" ];
  },
);

has logger => (is => 'ro', default => sub { sub { } });

=method index_site

  $search->index_site($site_dir, $writer);

This runs Pagefind over the HTML in C<$site_dir>, then writes its output into
F<pagefind/> through C<$writer>.

=cut

sub index_site ($self, $site_dir, $writer) {
  my $scratch = Path::Tiny->tempdir;

  my @cmd = (
    $self->command->@*,
    '--site', "$site_dir",
    '--output-path', "$scratch",
    '--silent',
  );

  system(@cmd) == 0 or die "pagefind failed: @cmd\n";

  my $iter = $scratch->iterator({ recurse => 1 });
  my $n = 0;
  while (my $file = $iter->()) {
    next if $file->is_dir;
    $writer->write_file('pagefind/' . $file->relative($scratch), $file->slurp_raw);
    $n++;
  }

  $self->logger->("search index: $n file(s)");
  return;
}

1;
