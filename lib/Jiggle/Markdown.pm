package Jiggle::Markdown;
use v5.36;

use Exporter 'import';
our @EXPORT_OK = qw(
  markdown_to_html markdown_to_text
  html_to_markdown escape_markdown
);

use CommonMark ();
use HTML::Parser ();

=head1 NAME

Jiggle::Markdown - render descriptions, and convert Flickr's into Markdown

=head1 SYNOPSIS

  my $html = markdown_to_html($description);
  my $text = markdown_to_text($description);

  my $markdown = html_to_markdown($flickr_description);

=head1 DESCRIPTION

Descriptions of photos and albums are Markdown, rendered as CommonMark with
one change from the standard: a newline is a line break, as it was on
Flickr, rather than being joined into the paragraph.

CommonMark's safe mode is on, as it is by default: raw HTML is omitted, and
links to C<javascript:> and similar URLs are neutered, so no description can
put markup of its own into a page.

=func markdown_to_html

This renders Markdown as HTML, as described above.

=func markdown_to_text

This returns the plain text of some Markdown, with the syntax gone, for
places like C<og:description>.

=cut

sub markdown_to_html ($markdown) {
  return '' unless defined $markdown and length $markdown;
  CommonMark->parse(string => $markdown)->render_html(CommonMark::OPT_HARDBREAKS);
}

sub markdown_to_text ($markdown) {
  return '' unless defined $markdown and length $markdown;

  my $iter = CommonMark->parse(string => $markdown)->iterator;
  my @parts;

  while (my ($event, $node) = $iter->next) {
    my $type = $node->get_type;

    if ($event == CommonMark::EVENT_ENTER) {
      push @parts, $node->get_literal
        if $type == CommonMark::NODE_TEXT or $type == CommonMark::NODE_CODE;
      push @parts, ' '
        if $type == CommonMark::NODE_SOFTBREAK or $type == CommonMark::NODE_LINEBREAK;
    } elsif ($type == CommonMark::NODE_PARAGRAPH or $type == CommonMark::NODE_HEADING) {
      push @parts, ' ';
    }
  }

  return join q{}, @parts;
}

=func html_to_markdown

  my $markdown = html_to_markdown($flickr_description);

Flickr descriptions are HTML, but only barely: mostly plain text, where a
newline means a line break, with the occasional link or bit of emphasis.
Since newlines are line breaks here too, converting is mostly a matter of
translating the few tags there are, and escaping any text that CommonMark
would otherwise take as syntax.

=for :list
* C<< <a href> >> becomes C<[text](url)>, or C<< <url> >> when the text is
the URL itself
* C<< <b> >> and C<< <strong> >> become C<**>; C<< <i> >> and C<< <em> >> become C<*>
* C<< <br> >> becomes a newline, and C<< <p> >> a blank line
* C<< <blockquote> >> becomes lines starting with C<< > >>
* any other tag is dropped, keeping its text
* a bare URL in text becomes an autolink, since Flickr made those clickable

=cut

my %EMPHASIS = (b => '**', strong => '**', i => '*', em => '*');

sub html_to_markdown ($html) {
  return '' unless defined $html and length $html;

  # Each open element that needs rewriting when it closes pushes a frame
  # here, recording where its content began in @out.
  my @out;
  my @stack;

  my $parser = HTML::Parser->new(
    api_version => 3,
    unbroken_text => 1,
    start_h => [ sub ($tag, $attr) {
      if ($tag eq 'a') {
        push @stack, { tag => 'a', href => $attr->{href}, at => scalar @out };
      } elsif ($EMPHASIS{$tag}) {
        push @out, $EMPHASIS{$tag};
      } elsif ($tag eq 'br') {
        push @out, "\n";
      } elsif ($tag eq 'p') {
        push @out, "\n\n";
      } elsif ($tag eq 'blockquote') {
        push @stack, { tag => 'blockquote', at => scalar @out };
      }
    }, 'tagname, attr' ],
    end_h => [ sub ($tag) {
      if ($EMPHASIS{$tag}) {
        push @out, $EMPHASIS{$tag};
        return;
      }

      push @out, "\n\n" if $tag eq 'p';

      return unless @stack and $stack[-1]{tag} eq $tag;
      my $frame = pop @stack;
      my $inner = join q{}, splice @out, $frame->{at};

      if ($tag eq 'a') {
        push @out, _link($inner, $frame->{href});
      } elsif ($tag eq 'blockquote') {
        $inner =~ s/\A\s+|\s+\z//g;
        push @out, "\n\n", (join "\n", map {; "> $_" } split /\n/, $inner), "\n\n";
      }
    }, 'tagname' ],
    text_h => [ sub ($text) {
      my $in_link = grep {; $_->{tag} eq 'a' } @stack;
      push @out, $in_link ? escape_markdown($text) : _autolink_and_escape($text);
    }, 'dtext' ],
  );

  $parser->parse($html);
  $parser->eof;

  # An unclosed element keeps its content, which is already in @out.
  my $md = join q{}, @out;

  $md =~ s/[ \t]+$//mg;
  $md =~ s/\n{3,}/\n\n/g;
  $md =~ s/\A\s+|\s+\z//g;

  return $md;
}

sub _link ($text, $href) {
  return $text unless defined $href and length $href;

  # The text was escaped on its way in; compare it with the escapes removed.
  (my $plain = $text) =~ s/\\(.)/$1/g;
  return "<$href>" if $plain eq $href;

  $href =~ s/([()\s])/sprintf '%%%02X', ord $1/ge;
  return "[$text]($href)";
}

my $URL = qr{https?://[^\s<>"]+};

sub _autolink_and_escape ($text) {
  my $md = q{};
  my $pos = 0;

  while ($text =~ /$URL/g) {
    my ($start, $url) = ($-[0], $&);

    # Trailing punctuation is almost always the sentence's, not the URL's.
    # It's left in the text that follows.
    $url =~ s/[.,;:!?')]+\z//;

    $md .= escape_markdown(substr $text, $pos, $start - $pos);
    $md .= "<$url>";
    $pos = $start + length $url;
    pos($text) = $pos;
  }

  return $md . escape_markdown(substr $text, $pos);
}

=func escape_markdown

  my $md = escape_markdown($plain_text);

This escapes plain text so that CommonMark renders it as the same text,
rather than taking any of it as syntax.  It escapes only where needed, so
the result stays readable: an underscore inside a word (like C<snake_case>)
can't start emphasis in CommonMark, so it's left alone.

=cut

sub escape_markdown ($text) {
  $text =~ s/([\\`*\[\]<])/\\$1/g;

  # Underscores only start or end emphasis next to a non-word character.
  $text =~ s/(?<![\p{Alnum}\\])_|_(?![\p{Alnum}])/\\_/g;

  # Something that looks like an entity would be decoded.
  $text =~ s/&(?=#?\w+;)/\\&/g;

  # Line starts that would begin a heading, quote, list, or rule.
  $text =~ s/^([ \t]*)([#>+=-])/$1\\$2/mg;
  $text =~ s/^([ \t]*\d+)([.)])(?=\s|\z)/$1\\$2/mg;

  return $text;
}

1;
