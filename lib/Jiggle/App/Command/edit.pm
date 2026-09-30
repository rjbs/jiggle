package Jiggle::App::Command::edit;
use v5.36;

use Jiggle::App -command;

use Jiggle::Editor;
use Jiggle::Query;
use Mojo::Server::Daemon;

sub abstract { 'review and edit a batch of photos in the browser' }

sub usage_desc { '%c edit %o QUERY...' }

sub description {
  return <<~'END';
  This picks a batch of photos and serves an editor for them on localhost.
  The batch is every photo matching all the query's terms:

    pending  private  public  all
    album:SLUG  tag:TAG  year:YYYY  id:ID

  For example, "jiggle edit pending" reviews newly ingested photos.

  END
}

sub opt_spec {
  return (
    [ 'port|p=i', 'port to listen on', { default => 3001 } ],
    [ 'no-open',  "don't open the editor in a browser" ],
  );
}

sub validate_args ($self, $opt, $args) {
  $self->usage_error('no query given; try "pending"') unless @$args;
}

sub execute ($self, $opt, $args) {
  my $library = $self->library;

  my $query = eval { Jiggle::Query->new({ library => $library, terms => $args }) };
  die $@ unless $query;

  my @ids = map {; $_->id } $query->photos;
  die "no photos match: @$args\n" unless @ids;

  my $editor = Jiggle::Editor->new({
    library => $library,
    ids     => \@ids,
    label   => "@$args",
  });

  my $listen = "http://127.0.0.1:" . $opt->port;
  my $url    = "$listen/?token=" . $editor->token;

  my $daemon = Mojo::Server::Daemon->new(app => $editor->app, listen => [ $listen ], silent => 1);
  $daemon->start;

  say sprintf '%d photo(s); editing at %s', 0 + @ids, $url;
  system('open', $url) if $^O eq 'darwin' && ! $opt->no_open;

  $daemon->ioloop->start;
}

1;
