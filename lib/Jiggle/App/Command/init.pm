package Jiggle::App::Command::init;
use v5.36;

use Jiggle::App -command;

use Jiggle::Library;
use Path::Tiny ();

sub abstract { 'make a new, empty library' }

sub usage_desc { '%c init %o DIR' }

sub opt_spec {
  return (
    [ 'title=s',    'the site title', { default => 'Photos' } ],
    [ 'base-url=s', 'the published site\'s URL, for links shared elsewhere',
                    { default => 'http://localhost:3000' } ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('give exactly one directory') unless @$args == 1;
  $self->usage_error("$args->[0]/jiggle.toml already exists")
    if -e "$args->[0]/jiggle.toml";
}

# The command needs no existing library, so it doesn't use the app's.
sub execute ($self, $opt, $args) {
  my $root = Path::Tiny::path($args->[0]);
  $root->child($_)->mkpath for qw( originals meta derived );

  my $str = sub ($s) { $s =~ s/(["\\])/\\$1/gr };

  $root->child('jiggle.toml')->spew_utf8(<<~"END");
    format   = $Jiggle::Library::FORMAT
    title    = "@{[ $str->($opt->title) ]}"
    base_url = "@{[ $str->($opt->base_url) ]}"

    # Every published location is rounded to this many decimal places; 3 is
    # about 100 meters.
    location_precision = 3

    # Photos taken inside a private zone publish no location at all.  The
    # radius is in meters.  Make them generous.
    #
    # [[private_zone]]
    # lat    = 40.0
    # lon    = -75.0
    # radius = 500
    END

  say "made a library at $root";
  say "consider making meta/ a git repository: git -C $root/meta init";
}

1;
