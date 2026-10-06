package Jiggle::App::Command::sheet;
use v5.36;

use Jiggle::App -command;

use Jiggle::Query;
use Jiggle::Sheet;
use Path::Tiny ();

sub abstract { 'make a local contact sheet of some photos, and open it' }

sub usage_desc { '%c sheet %o QUERY...' }

sub description {
  return <<~'END';
  This writes one HTML page of the photos matching all the query's terms
  (the same as jiggle edit takes), and opens it.  It shows the library's
  files directly, so it works only on this machine, and is never published.

    pending  private  public  unlisted  all
    album:SLUG  tag:TAG  year:YYYY  id:ID  limit:N

  For example, "jiggle sheet private --group flickr-privacy" is the page for
  reviewing private photos.  The page goes in the library's .jiggle/sheets/
  unless --output says otherwise.

  END
}

sub opt_spec {
  return (
    [ 'output|o=s', 'where to write the page' ],
    [ 'group|g=s',  'group by: ' . join(', ', Jiggle::Sheet->groupings) ],
    [ 'no-open',    "don't open the page" ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('no query given; try "private"') unless @$args;
  $self->usage_error('can only group by ' . join(', ', Jiggle::Sheet->groupings))
    if $opt->group and ! grep {; $_ eq $opt->group } Jiggle::Sheet->groupings;
}

sub execute ($self, $opt, $args) {
  my $library = $self->library;
  my @photos  = Jiggle::Query->new({ library => $library, terms => $args })->photos;
  die "no photos match: @$args\n" unless @photos;

  my $label = "@$args";
  (my $name = lc $label) =~ s/[^a-z0-9]+/-/g;
  $name =~ s/\A-|-\z//g;

  my $out = Path::Tiny::path($opt->output // $library->state_dir->child('sheets', "$name.html"))->absolute;
  $out->parent->mkpath;

  my $site = $library->root->child('site');
  die "refusing to write inside $site, which gets published\n"
    if -d $site and $site->realpath->subsumes($out->parent->realpath);

  $out->spew_utf8(Jiggle::Sheet->new({ library => $library, label => $label })
    ->html(\@photos, { group => $opt->group }));

  say sprintf 'wrote %s: %d photo(s)', $out, 0 + @photos;
  system('open', "$out") if $^O eq 'darwin' && ! $opt->no_open;
}

1;
